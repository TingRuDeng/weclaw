package agent

import (
	"bytes"
	"fmt"
)

func parseNullTerminatedCodexHostArgs(data []byte) ([]string, error) {
	parts := bytes.Split(data, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) == 0 || len(parts[0]) == 0 {
		return nil, fmt.Errorf("原始参数为空")
	}
	args := make([]string, len(parts))
	for index, part := range parts {
		args[index] = string(part)
	}
	return args, nil
}
