package messaging

import (
	"bufio"
	"io"
	"os"
	"strings"
)

const unknownSessionModelValue = "未知（会话未记录）"

type sessionModelStatus struct {
	Model  string
	Effort string
}

// renderSessionModelStatus 始终展示两个字段，避免把当前全局配置误认为历史会话状态。
func renderSessionModelStatus(status sessionModelStatus) []string {
	return []string{
		"模型: " + recordedSessionValue(status.Model),
		"推理强度: " + recordedSessionValue(status.Effort),
	}
}

// renderCompactSessionModelStatus 用于导航结果，把相关配置压缩到同一行。
func renderCompactSessionModelStatus(status sessionModelStatus) string {
	return "模型: " + recordedSessionValue(status.Model) + " · 推理强度: " + recordedSessionValue(status.Effort)
}

func recordedSessionValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return unknownSessionModelValue
}

// readSessionJSONLines 逐行读取大体积 transcript，避免 Scanner 的默认行长度限制。
func readSessionJSONLines(path string, visit func([]byte)) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			visit(line)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return
			}
			return
		}
	}
}
