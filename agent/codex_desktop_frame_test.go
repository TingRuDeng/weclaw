package agent

import (
	"bytes"
	"encoding/binary"
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
