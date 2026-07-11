package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCodexDesktopFrameReadsFragmentedPayload(t *testing.T) {
	payload := []byte(`{"type":"broadcast","method":"thread-stream-state-changed"}`)
	reader, writer := io.Pipe()
	writeErr := make(chan error, 1)
	go func() {
		defer close(writeErr)
		var header [codexDesktopFrameHeaderBytes]byte
		binary.LittleEndian.PutUint32(header[:], uint32(len(payload)))
		for _, part := range [][]byte{header[:2], header[2:], payload[:7], payload[7:]} {
			if _, err := writer.Write(part); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- writer.Close()
	}()

	got, err := readCodexDesktopFrame(reader)
	if err != nil {
		t.Fatalf("readCodexDesktopFrame() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("readCodexDesktopFrame() = %q, want %q", got, payload)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("fragmented writer error = %v", err)
	}
}

func TestCodexDesktopFrameReadsCoalescedFrames(t *testing.T) {
	first := []byte(`{"type":"request","requestId":"one"}`)
	second := []byte(`{"type":"response","requestId":"one"}`)
	var stream bytes.Buffer
	writeRawCodexDesktopTestFrame(t, &stream, first)
	writeRawCodexDesktopTestFrame(t, &stream, second)

	for index, want := range [][]byte{first, second} {
		got, err := readCodexDesktopFrame(&stream)
		if err != nil {
			t.Fatalf("frame %d read error = %v", index, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d = %q, want %q", index, got, want)
		}
	}
}

func TestCodexDesktopFrameRejectsOversizedPayload(t *testing.T) {
	var header [codexDesktopFrameHeaderBytes]byte
	binary.LittleEndian.PutUint32(header[:], uint32(codexDesktopMaxFrameBytes+1))

	_, err := readCodexDesktopFrame(bytes.NewReader(header[:]))
	if err == nil || !strings.Contains(err.Error(), "超出上限") {
		t.Fatalf("readCodexDesktopFrame() error = %v, want oversized error", err)
	}
}

func TestCodexDesktopFrameRejectsTruncatedPayload(t *testing.T) {
	var stream bytes.Buffer
	var header [codexDesktopFrameHeaderBytes]byte
	binary.LittleEndian.PutUint32(header[:], 8)
	stream.Write(header[:])
	stream.WriteString("short")

	_, err := readCodexDesktopFrame(&stream)
	if err == nil || !strings.Contains(err.Error(), "payload") {
		t.Fatalf("readCodexDesktopFrame() error = %v, want truncated payload error", err)
	}
}

func TestCodexDesktopFrameRejectsEmptyPayload(t *testing.T) {
	var header [codexDesktopFrameHeaderBytes]byte
	if _, err := readCodexDesktopFrame(bytes.NewReader(header[:])); err == nil {
		t.Fatal("readCodexDesktopFrame() error = nil, want empty payload error")
	}
	if err := writeCodexDesktopFrame(io.Discard, nil); err == nil {
		t.Fatal("writeCodexDesktopFrame() error = nil, want empty payload error")
	}
}

func TestCodexDesktopFrameRejectsTruncatedHeader(t *testing.T) {
	_, err := readCodexDesktopFrame(bytes.NewReader([]byte{1, 0}))
	if err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("readCodexDesktopFrame() error = %v, want truncated header error", err)
	}
}

func TestCodexDesktopFrameWritesLengthPrefixedPayload(t *testing.T) {
	payload := []byte(`{"type":"request"}`)
	var got bytes.Buffer

	if err := writeCodexDesktopFrame(&got, payload); err != nil {
		t.Fatalf("writeCodexDesktopFrame() error = %v", err)
	}
	if binary.LittleEndian.Uint32(got.Bytes()[:codexDesktopFrameHeaderBytes]) != uint32(len(payload)) {
		t.Fatalf("written header = %v, want payload length %d", got.Bytes()[:codexDesktopFrameHeaderBytes], len(payload))
	}
	if !bytes.Equal(got.Bytes()[codexDesktopFrameHeaderBytes:], payload) {
		t.Fatalf("written payload = %q, want %q", got.Bytes()[codexDesktopFrameHeaderBytes:], payload)
	}
}

func TestCodexDesktopFrameRejectsInvalidJSONPayload(t *testing.T) {
	payload := []byte("not-json")
	t.Run("read", func(t *testing.T) {
		var stream bytes.Buffer
		writeRawCodexDesktopTestFrame(t, &stream, payload)
		if _, err := readCodexDesktopFrame(&stream); err == nil {
			t.Fatal("readCodexDesktopFrame() error = nil, want invalid JSON error")
		}
	})
	t.Run("write", func(t *testing.T) {
		if err := writeCodexDesktopFrame(io.Discard, payload); err == nil {
			t.Fatal("writeCodexDesktopFrame() error = nil, want invalid JSON error")
		}
	})
}

func TestCodexDesktopFrameWriterHandlesPartialWrites(t *testing.T) {
	payload := []byte(`{"type":"request"}`)
	writer := &codexDesktopPartialWriter{maxBytes: 2}
	if err := writeCodexDesktopFrame(writer, payload); err != nil {
		t.Fatalf("writeCodexDesktopFrame() error = %v", err)
	}
	var want bytes.Buffer
	writeRawCodexDesktopTestFrame(t, &want, payload)
	if !bytes.Equal(writer.buffer.Bytes(), want.Bytes()) {
		t.Fatalf("partial writer output = %v, want %v", writer.buffer.Bytes(), want.Bytes())
	}
}

func TestCodexDesktopFrameWriterRejectsZeroWrite(t *testing.T) {
	writer := codexDesktopWriterFunc(func([]byte) (int, error) { return 0, nil })
	err := writeCodexDesktopFrame(writer, []byte(`{"type":"request"}`))
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("writeCodexDesktopFrame() error = %v, want io.ErrShortWrite", err)
	}
}

func TestCodexDesktopFrameWriterRejectsInvalidWriteCount(t *testing.T) {
	tests := []struct {
		name  string
		count func(int) int
	}{
		{"negative", func(int) int { return -1 }},
		{"larger than input", func(length int) int { return length + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := codexDesktopWriterFunc(func(data []byte) (int, error) {
				return test.count(len(data)), nil
			})
			err := writeCodexDesktopFrame(writer, []byte(`{"type":"request"}`))
			if err == nil || !strings.Contains(err.Error(), "非法") || !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("writeCodexDesktopFrame() error = %v, want explicit invalid count error", err)
			}
		})
	}
}

func TestCodexDesktopFrameRejectsNonUTF8Payload(t *testing.T) {
	payload := []byte{'"', 0xff, '"'}
	t.Run("read", func(t *testing.T) {
		var stream bytes.Buffer
		writeRawCodexDesktopTestFrame(t, &stream, payload)
		if _, err := readCodexDesktopFrame(&stream); err == nil || !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("readCodexDesktopFrame() error = %v, want UTF-8 error", err)
		}
	})
	t.Run("write", func(t *testing.T) {
		if err := writeCodexDesktopFrame(io.Discard, payload); err == nil || !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("writeCodexDesktopFrame() error = %v, want UTF-8 error", err)
		}
	})
}

func writeRawCodexDesktopTestFrame(t *testing.T, writer io.Writer, payload []byte) {
	t.Helper()
	var header [codexDesktopFrameHeaderBytes]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		t.Fatalf("write frame header: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write frame payload: %v", err)
	}
}

type codexDesktopPartialWriter struct {
	buffer   bytes.Buffer
	maxBytes int
}

func (writer *codexDesktopPartialWriter) Write(data []byte) (int, error) {
	if len(data) > writer.maxBytes {
		data = data[:writer.maxBytes]
	}
	return writer.buffer.Write(data)
}

type codexDesktopWriterFunc func([]byte) (int, error)

func (write codexDesktopWriterFunc) Write(data []byte) (int, error) {
	return write(data)
}
