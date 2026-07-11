package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const agentSessionStateVersion = 1

type agentSessionStore struct {
	mu         sync.RWMutex
	filePath   string
	selections map[string]string
}

type agentSessionState struct {
	Version    int               `json:"version"`
	Selections map[string]string `json:"selections"`
	Updated    string            `json:"updated"`
}

func newAgentSessionStore() *agentSessionStore {
	return &agentSessionStore{selections: make(map[string]string)}
}

// DefaultAgentSessionFile 返回会话级默认 Agent 的持久化路径。
func DefaultAgentSessionFile() string {
	return filepath.Join(defaultDataDir(), "agent-sessions.json")
}

// SetFilePath 切换持久化文件，并用文件中的有效状态替换当前内存状态。
func (s *agentSessionStore) SetFilePath(filePath string) error {
	filePath = strings.TrimSpace(filePath)
	s.mu.Lock()
	s.filePath = filePath
	s.mu.Unlock()
	selections, err := loadAgentSessionSelections(filePath)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.selections = selections
	s.mu.Unlock()
	return nil
}

// Get 查询指定会话显式选择的默认 Agent。
func (s *agentSessionStore) Get(routeUserID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	agentName, ok := s.selections[strings.TrimSpace(routeUserID)]
	return agentName, ok
}

// Set 原子持久化会话选择；写盘失败时不污染内存状态。
func (s *agentSessionStore) Set(routeUserID string, agentName string) error {
	routeUserID = strings.TrimSpace(routeUserID)
	agentName = strings.TrimSpace(agentName)
	if routeUserID == "" || agentName == "" {
		return fmt.Errorf("会话键和 Agent 名称不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := cloneAgentSelections(s.selections)
	next[routeUserID] = agentName
	if s.filePath != "" {
		if err := saveAgentSessionSelections(s.filePath, next); err != nil {
			return err
		}
	}
	s.selections = next
	return nil
}

func loadAgentSessionSelections(filePath string) (map[string]string, error) {
	selections := make(map[string]string)
	if filePath == "" {
		return selections, nil
	}
	data, err := os.ReadFile(filePath)
	if os.IsNotExist(err) {
		return selections, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取会话 Agent 状态失败: %w", err)
	}
	var state agentSessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析会话 Agent 状态失败: %w", err)
	}
	if state.Version != agentSessionStateVersion {
		return nil, fmt.Errorf("不支持的会话 Agent 状态版本: %d", state.Version)
	}
	for routeUserID, agentName := range state.Selections {
		if key, value := strings.TrimSpace(routeUserID), strings.TrimSpace(agentName); key != "" && value != "" {
			selections[key] = value
		}
	}
	return selections, nil
}

func saveAgentSessionSelections(filePath string, selections map[string]string) error {
	if filePath == "" {
		return fmt.Errorf("会话 Agent 状态文件未配置")
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("创建会话 Agent 状态目录失败: %w", err)
	}
	state := agentSessionState{Version: agentSessionStateVersion, Selections: selections, Updated: time.Now().UTC().Format(time.RFC3339)}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("编码会话 Agent 状态失败: %w", err)
	}
	return writeAgentSessionStateAtomically(filePath, data)
}

func writeAgentSessionStateAtomically(filePath string, data []byte) error {
	tmpFile, err := os.CreateTemp(filepath.Dir(filePath), filepath.Base(filePath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建会话 Agent 临时文件失败: %w", err)
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("设置会话 Agent 文件权限失败: %w", err)
	}
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("写入会话 Agent 状态失败: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("关闭会话 Agent 状态失败: %w", err)
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("替换会话 Agent 状态失败: %w", err)
	}
	return nil
}

func cloneAgentSelections(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}
