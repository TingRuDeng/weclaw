package agent

import (
	"encoding/json"
	"strings"
)

type codexFileUpdateChange struct {
	Path string `json:"path"`
}

// codexFileProgressLine 只接受结构化文件身份；patch/output delta 不进入用户进度。
func codexFileProgressLine(p codexProgressParams) string {
	if _, ok := firstCodexFileChange(p.Changes); ok {
		return "修改代码"
	}
	if path := firstCodexFilePath(p); path != "" {
		return "修改代码"
	}
	return ""
}

func codexFileProgressEvent(p codexProgressParams, line string) *codexProgressEvent {
	event := &codexProgressEvent{
		ID: "file:changes", Kind: "file", Action: line, Status: p.Status,
	}
	if change, ok := firstCodexFileChange(p.Changes); ok {
		event.FilePath = strings.TrimSpace(change.Path)
		return event
	}
	if path := firstCodexFilePath(p); path != "" {
		event.FilePath = path
		return event
	}
	return event
}

func firstCodexFilePath(p codexProgressParams) string {
	for _, path := range append([]string{p.FilePath, p.Path}, append(p.Files, p.Paths...)...) {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstCodexFileChange(raw json.RawMessage) (codexFileUpdateChange, bool) {
	var changes []codexFileUpdateChange
	if json.Unmarshal(raw, &changes) != nil || len(changes) == 0 || strings.TrimSpace(changes[0].Path) == "" {
		return codexFileUpdateChange{}, false
	}
	return changes[0], true
}
