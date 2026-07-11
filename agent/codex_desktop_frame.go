package agent

import (
	"encoding/binary"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	codexDesktopFrameHeaderBytes = 4
	codexDesktopMaxFrameBytes    = 256 << 20
)

func readCodexDesktopFrame(reader io.Reader) ([]byte, error) {
	var header [codexDesktopFrameHeaderBytes]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, fmt.Errorf("读取 Codex Desktop frame header: %w", err)
	}

	length := uint64(binary.LittleEndian.Uint32(header[:]))
	if err := validateCodexDesktopFrameLength(length); err != nil {
		return nil, err
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("读取 Codex Desktop frame payload: %w", err)
	}
	if !utf8.Valid(payload) {
		return nil, fmt.Errorf("Codex Desktop frame payload 不是有效 UTF-8")
	}
	return payload, nil
}

func writeCodexDesktopFrame(writer io.Writer, payload []byte) error {
	if err := validateCodexDesktopFramePayload(payload); err != nil {
		return err
	}
	var header [codexDesktopFrameHeaderBytes]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeCodexDesktopFrameBytes(writer, header[:]); err != nil {
		return fmt.Errorf("写入 Codex Desktop frame header: %w", err)
	}
	if err := writeCodexDesktopFrameBytes(writer, payload); err != nil {
		return fmt.Errorf("写入 Codex Desktop frame payload: %w", err)
	}
	return nil
}

func validateCodexDesktopFrameLength(length uint64) error {
	if length == 0 {
		return fmt.Errorf("Codex Desktop frame payload 不能为空")
	}
	if length > codexDesktopMaxFrameBytes {
		return fmt.Errorf("Codex Desktop frame payload 长度 %d 超出上限 %d", length, codexDesktopMaxFrameBytes)
	}
	return nil
}

func validateCodexDesktopFramePayload(payload []byte) error {
	if err := validateCodexDesktopFrameLength(uint64(len(payload))); err != nil {
		return err
	}
	if !utf8.Valid(payload) {
		return fmt.Errorf("Codex Desktop frame payload 不是有效 UTF-8")
	}
	return nil
}

func writeCodexDesktopFrameBytes(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
