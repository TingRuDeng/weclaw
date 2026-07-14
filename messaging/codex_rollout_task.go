package messaging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	codexRolloutTaskStarted = "task_started"
	codexRolloutTaskDone    = "task_complete"
	codexRolloutTurnAborted = "turn_aborted"
	codexRolloutUserMessage = "user_message"
	codexRolloutProgress    = "progress"
)

var codexRolloutRelevantMarkers = [][]byte{
	[]byte(`"task_started"`), []byte(`"task_complete"`), []byte(`"turn_aborted"`),
	[]byte(`"user_message"`), []byte(`"agent_message"`), []byte(`"agent_reasoning"`),
	[]byte(`"role":"user"`), []byte(`"role": "user"`),
}

type codexRolloutTaskState struct {
	Path     string
	TurnID   string
	Preview  string
	Progress string
	Final    string
	Reason   string
	Offset   int64
	Size     int64
	Active   bool
	Aborted  bool
}

type codexRolloutEvent struct {
	Kind   string
	TurnID string
	Text   string
	Reason string
}

type codexRolloutRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type             string `json:"type"`
		TurnID           string `json:"turn_id"`
		Message          string `json:"message"`
		Text             string `json:"text"`
		Phase            string `json:"phase"`
		Role             string `json:"role"`
		LastAgentMessage string `json:"last_agent_message"`
		Reason           string `json:"reason"`
		Content          []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Metadata struct {
			TurnID string `json:"turn_id"`
		} `json:"internal_chat_message_metadata_passthrough"`
	} `json:"payload"`
}

// readCodexRolloutTaskState 顺序扫描一次 rollout，只保留最新 turn 的最小展示状态。
func readCodexRolloutTaskState(path string) (codexRolloutTaskState, error) {
	state := codexRolloutTaskState{Path: strings.TrimSpace(path)}
	offset, err := readCodexRolloutEvents(state.Path, 0, func(event codexRolloutEvent) error {
		applyCodexRolloutEvent(&state, event)
		return nil
	})
	state.Offset = offset
	if err := finalizeCodexRolloutState(&state, err); err != nil {
		return state, err
	}
	return state, nil
}

// readCodexRolloutTaskStateForTurn 单次扫描目标 turn，忽略它之前的历史任务。
func readCodexRolloutTaskStateForTurn(path string, turnID string) (codexRolloutTaskState, bool, error) {
	state := codexRolloutTaskState{Path: strings.TrimSpace(path)}
	target := strings.TrimSpace(turnID)
	found := false
	offset, err := readCodexRolloutEvents(state.Path, 0, func(event codexRolloutEvent) error {
		if !found {
			if event.Kind != codexRolloutTaskStarted || event.TurnID != target {
				return nil
			}
			found = true
		}
		if event.Kind == codexRolloutTaskStarted && event.TurnID != target && state.Active {
			return fmt.Errorf("%w: %s", errCodexRolloutTurnChanged, event.TurnID)
		}
		if event.Kind != codexRolloutTaskStarted || event.TurnID == target {
			applyCodexRolloutEvent(&state, event)
		}
		return nil
	})
	state.Offset = offset
	if err := finalizeCodexRolloutState(&state, err); err != nil {
		return state, found, err
	}
	return state, found, nil
}

// finalizeCodexRolloutState 记录扫描结束时的文件大小，供控制权移交核对稳定检查点。
func finalizeCodexRolloutState(state *codexRolloutTaskState, scanErr error) error {
	if scanErr != nil {
		return scanErr
	}
	info, err := os.Stat(state.Path)
	if err != nil {
		return fmt.Errorf("读取 Codex rollout checkpoint 失败: %w", err)
	}
	if info.Size() < state.Offset {
		return fmt.Errorf("codex rollout 已被截断")
	}
	state.Size = info.Size()
	return nil
}

// applyCodexRolloutEvent 将单个共享事件归并到最新 turn 状态。
func applyCodexRolloutEvent(state *codexRolloutTaskState, event codexRolloutEvent) {
	switch event.Kind {
	case codexRolloutTaskStarted:
		*state = codexRolloutTaskState{Path: state.Path, TurnID: event.TurnID, Active: event.TurnID != ""}
	case codexRolloutTaskDone:
		if event.TurnID == state.TurnID {
			state.Active = false
			state.Final = event.Text
		}
	case codexRolloutTurnAborted:
		if event.TurnID == state.TurnID {
			state.Active = false
			state.Aborted = true
			state.Reason = event.Reason
		}
	case codexRolloutUserMessage:
		if state.Active && (event.TurnID == "" || event.TurnID == state.TurnID) {
			state.Preview = event.Text
		}
	case codexRolloutProgress:
		if state.Active {
			state.Progress = event.Text
		}
	}
}

// readCodexRolloutEvents 从指定偏移读取完整 JSONL 行，半行不会推进偏移。
func readCodexRolloutEvents(path string, offset int64, visit func(codexRolloutEvent) error) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return offset, fmt.Errorf("打开 Codex rollout 失败: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return offset, fmt.Errorf("读取 Codex rollout 状态失败: %w", err)
	}
	if info.Size() < offset {
		return offset, fmt.Errorf("codex rollout 已被截断")
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, fmt.Errorf("定位 Codex rollout 失败: %w", err)
	}
	reader := bufio.NewReader(file)
	current := offset
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			current += int64(len(line))
			event, relevant, parseErr := parseCodexRolloutEvent(line)
			if parseErr != nil {
				return current, parseErr
			}
			if relevant {
				if err := visit(event); err != nil {
					return current, err
				}
			}
		}
		if readErr == io.EOF {
			return current, nil
		}
		if readErr != nil {
			return current, fmt.Errorf("读取 Codex rollout 失败: %w", readErr)
		}
	}
}

// parseCodexRolloutEvent 仅解析任务镜像需要的最小事件集合。
func parseCodexRolloutEvent(line []byte) (codexRolloutEvent, bool, error) {
	if !codexRolloutLineRelevant(line) {
		return codexRolloutEvent{}, false, nil
	}
	var record codexRolloutRecord
	if err := json.Unmarshal(line, &record); err != nil {
		return codexRolloutEvent{}, false, fmt.Errorf("解析 Codex rollout 失败: %w", err)
	}
	payload := record.Payload
	switch payload.Type {
	case codexRolloutTaskStarted:
		return codexRolloutEvent{Kind: codexRolloutTaskStarted, TurnID: strings.TrimSpace(payload.TurnID)}, true, nil
	case codexRolloutTaskDone:
		return codexRolloutEvent{Kind: codexRolloutTaskDone, TurnID: strings.TrimSpace(payload.TurnID), Text: strings.TrimSpace(payload.LastAgentMessage)}, true, nil
	case codexRolloutTurnAborted:
		return codexRolloutEvent{Kind: codexRolloutTurnAborted, TurnID: strings.TrimSpace(payload.TurnID), Reason: strings.TrimSpace(payload.Reason)}, true, nil
	case "agent_message":
		if strings.TrimSpace(payload.Phase) == "commentary" {
			return codexRolloutEvent{Kind: codexRolloutProgress, Text: strings.TrimSpace(payload.Message)}, true, nil
		}
	case "agent_reasoning":
		return codexRolloutEvent{Kind: codexRolloutProgress, Text: strings.TrimSpace(payload.Text)}, true, nil
	case codexRolloutUserMessage:
		return codexRolloutEvent{Kind: codexRolloutUserMessage, TurnID: strings.TrimSpace(payload.TurnID), Text: firstNonBlank(payload.Message, payload.Text)}, true, nil
	case "message":
		if strings.TrimSpace(payload.Role) == "user" {
			return codexRolloutEvent{Kind: codexRolloutUserMessage, TurnID: strings.TrimSpace(payload.Metadata.TurnID), Text: codexRolloutContentText(payload.Content)}, true, nil
		}
	}
	return codexRolloutEvent{}, false, nil
}

// codexRolloutLineRelevant 在反序列化前快速过滤无关的大体积记录。
func codexRolloutLineRelevant(line []byte) bool {
	for _, marker := range codexRolloutRelevantMarkers {
		if bytes.Contains(line, marker) {
			return true
		}
	}
	return false
}

// codexRolloutContentText 合并用户消息的文本内容，忽略图片等非文本项。
func codexRolloutContentText(content []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if text := strings.TrimSpace(item.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

// findLocalCodexRolloutPath 在用户主会话目录中定位指定 thread 的 rollout。
func findLocalCodexRolloutPath(codexDir string, threadID string) (string, bool, error) {
	threadID = strings.TrimSpace(threadID)
	root := filepath.Join(strings.TrimSpace(codexDir), "sessions")
	if threadID == "" || strings.TrimSpace(codexDir) == "" {
		return "", false, nil
	}
	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" || !strings.Contains(filepath.Base(path), threadID) {
			return nil
		}
		meta, ok := readLocalCodexSessionMeta(path)
		if ok && meta.ID == threadID {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil && err != fs.SkipAll {
		return "", false, fmt.Errorf("扫描 Codex rollout 失败: %w", err)
	}
	return found, found != "", nil
}

// readLocalCodexRolloutTaskState 读取 Handler 配置目录中指定 thread 的共享 rollout 状态。
func (h *Handler) readLocalCodexRolloutTaskState(threadID string) (codexRolloutTaskState, bool, error) {
	h.mu.RLock()
	dir := h.codexLocalSessionDir
	h.mu.RUnlock()
	path, found, err := findLocalCodexRolloutPath(dir, threadID)
	if err != nil || !found {
		return codexRolloutTaskState{}, found, err
	}
	state, err := readCodexRolloutTaskState(path)
	return state, true, err
}
