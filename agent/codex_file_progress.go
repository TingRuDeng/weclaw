package agent

import (
	"encoding/json"
	"strings"
)

type codexFileUpdateChange struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
	Kind struct {
		Type string `json:"type"`
	} `json:"kind"`
}

// codexFileProgressLine 优先显示结构化文件动作，避免把 patch 原文塞进卡片。
func codexFileProgressLine(p codexProgressParams) string {
	if change, ok := firstCodexFileChange(p.Changes); ok {
		return codexFileAction(change) + " " + strings.TrimSpace(change.Path)
	}
	if path := firstCodexFilePath(p); path != "" {
		return "修改 " + path
	}
	text := firstNonEmpty(codexChangesText(p.Changes), p.Diff, p.Message, p.Text, p.Output, p.Delta)
	if path := filePathFromPatchText(text); path != "" {
		return "修改 " + path
	}
	return latestCodexRealtimeLine(p)
}

func codexFileProgressEvent(p codexProgressParams, line string) *codexProgressEvent {
	event := &codexProgressEvent{Kind: "file", Action: line}
	if change, ok := firstCodexFileChange(p.Changes); ok {
		event.FilePath = strings.TrimSpace(change.Path)
		return event
	}
	if path := firstCodexFilePath(p); path != "" {
		event.FilePath = path
		return event
	}
	text := firstNonEmpty(codexChangesText(p.Changes), p.Diff, p.Message, p.Text, p.Output, p.Delta)
	event.FilePath = filePathFromPatchText(text)
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

func codexFileAction(change codexFileUpdateChange) string {
	switch strings.TrimSpace(change.Kind.Type) {
	case "add":
		return "新增"
	case "delete":
		return "删除"
	default:
		return "修改"
	}
}

func codexChangesText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	return ""
}

func filePathFromPatchText(text string) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Update File:", "*** Add File:", "*** Delete File:"} {
			if path := strings.TrimSpace(strings.TrimPrefix(line, prefix)); path != line {
				return path
			}
		}
	}
	return ""
}
