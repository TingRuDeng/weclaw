package agent

import (
	"fmt"
	"strings"
	"unicode"
)

type claudeLoadedSessionState struct {
	Cwd        string
	Generation uint64
}

type claudeSessionCommandState struct {
	Generation uint64
	Sequence   uint64
	Known      bool
	Names      map[string]struct{}
	Err        string
}

type claudeSessionTitleState struct {
	Generation uint64
	Sequence   uint64
	Known      bool
	Title      string
}

func (a *ACPAgent) resetClaudeHostSessionStateLocked() {
	clear(a.claudeLoadedSessions)
	clear(a.claudeSessionCommands)
	clear(a.claudeSessionTitles)
	a.signalClaudeCommandChangeLocked()
}

func (a *ACPAgent) signalClaudeCommandChangeLocked() {
	if a.claudeCommandChanged != nil {
		close(a.claudeCommandChanged)
	}
	a.claudeCommandChanged = make(chan struct{})
}

func (a *ACPAgent) cacheClaudeSessionMetadataUpdate(sessionID string, update sessionUpdate) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session/update sessionId 不能为空")
	}
	switch update.SessionUpdate {
	case "available_commands_update":
		names, err := validateACPAvailableCommands(update.AvailableCommands)
		a.mu.Lock()
		current := a.claudeSessionCommands[sessionID]
		if update.Sequence == 0 || current.Sequence == 0 || update.Sequence >= current.Sequence {
			state := claudeSessionCommandState{
				Generation: a.legacyRuntimeGeneration,
				Sequence:   update.Sequence,
				Known:      true,
				Names:      names,
			}
			if err != nil {
				state.Err = err.Error()
			}
			a.claudeSessionCommands[sessionID] = state
			a.signalClaudeCommandChangeLocked()
		}
		a.mu.Unlock()
		return err
	case "session_info_update":
		a.mu.Lock()
		current := a.claudeSessionTitles[sessionID]
		if update.Sequence == 0 || current.Sequence == 0 || update.Sequence >= current.Sequence {
			a.claudeSessionTitles[sessionID] = claudeSessionTitleState{
				Generation: a.legacyRuntimeGeneration,
				Sequence:   update.Sequence,
				Known:      true,
				Title:      update.Title,
			}
		}
		a.mu.Unlock()
	}
	return nil
}

func validateACPAvailableCommands(commands []acpAvailableCommand) (map[string]struct{}, error) {
	if commands == nil {
		return nil, fmt.Errorf("available_commands_update 缺少 availableCommands 数组")
	}
	names := make(map[string]struct{}, len(commands))
	for index, command := range commands {
		name := command.Name
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || strings.HasPrefix(name, "/") {
			return nil, fmt.Errorf("availableCommands[%d].name 无效", index)
		}
		for _, value := range name {
			if unicode.IsControl(value) {
				return nil, fmt.Errorf("availableCommands[%d].name 包含控制字符", index)
			}
		}
		if _, duplicate := names[name]; duplicate {
			return nil, fmt.Errorf("availableCommands 包含重复命令 %q", name)
		}
		names[name] = struct{}{}
	}
	return names, nil
}

func (a *ACPAgent) markClaudeSessionLoaded(sessionID string, cwd string) {
	sessionID = strings.TrimSpace(sessionID)
	cwd = strings.TrimSpace(cwd)
	if sessionID == "" || cwd == "" {
		return
	}
	a.mu.Lock()
	a.claudeLoadedSessions[sessionID] = claudeLoadedSessionState{
		Cwd: cwd, Generation: a.legacyRuntimeGeneration,
	}
	a.mu.Unlock()
}
