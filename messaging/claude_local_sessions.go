package messaging

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const claudeLocalSource = "local"

type localClaudeProjectConfig struct {
	Projects map[string]json.RawMessage `json:"projects"`
}

type localClaudeTranscriptSummary struct {
	Summary   string `json:"summary"`
	Timestamp string `json:"timestamp"`
}

// defaultClaudeLocalSessionDir 返回本机 Claude Code 默认数据目录。
func defaultClaudeLocalSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// discoverLocalClaudeSessions 只读取 Claude transcript 的首行摘要和文件元数据。
func discoverLocalClaudeSessions(claudeDir string) []codexWorkspaceView {
	views, _ := discoverLocalClaudeSessionSnapshot(claudeDir)
	return views
}

func discoverLocalClaudeSessionSnapshot(claudeDir string) ([]codexWorkspaceView, map[string]map[string]bool) {
	claudeDir = strings.TrimSpace(claudeDir)
	if claudeDir == "" {
		return nil, map[string]map[string]bool{}
	}
	views := make([]codexWorkspaceView, 0)
	visibleByWorkspace := map[string]map[string]bool{}
	for _, workspaceRoot := range discoverLocalClaudeWorkspaceRoots(claudeDir) {
		sessions := readLocalClaudeProjectSessions(claudeDir, workspaceRoot)
		visibleByWorkspace[workspaceRoot] = localClaudeVisibleSessionSet(sessions)
		views = append(views, sessions...)
	}
	sortLocalCodexSessions(views)
	return views, visibleByWorkspace
}

func discoverLocalClaudeWorkspaceRoots(claudeDir string) []string {
	projects := readLocalClaudeProjects(claudeDir)
	roots := make([]string, 0, len(projects))
	for workspaceRoot := range projects {
		workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
		if workspaceRoot == "" || !localClaudeWorkspaceExists(workspaceRoot) {
			continue
		}
		roots = append(roots, workspaceRoot)
	}
	return roots
}

func localClaudeVisibleSessionSet(sessions []codexWorkspaceView) map[string]bool {
	visible := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		sessionID := strings.TrimSpace(session.ThreadID)
		if sessionID != "" {
			visible[sessionID] = true
		}
	}
	return visible
}

func readLocalClaudeProjects(claudeDir string) map[string]json.RawMessage {
	for _, path := range localClaudeConfigCandidates(claudeDir) {
		projects := readLocalClaudeProjectConfig(path)
		if len(projects) > 0 {
			return projects
		}
	}
	return map[string]json.RawMessage{}
}

func localClaudeConfigCandidates(claudeDir string) []string {
	candidates := []string{filepath.Join(claudeDir, "claude.json")}
	if filepath.Base(claudeDir) == ".claude" {
		candidates = append(candidates, filepath.Join(filepath.Dir(claudeDir), ".claude.json"))
	}
	return candidates
}

func readLocalClaudeProjectConfig(path string) map[string]json.RawMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]json.RawMessage{}
	}
	var cfg localClaudeProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Projects == nil {
		return map[string]json.RawMessage{}
	}
	return cfg.Projects
}

func readLocalClaudeProjectSessions(claudeDir string, workspaceRoot string) []codexWorkspaceView {
	projectDir := filepath.Join(claudeDir, "projects", encodeClaudeProjectPath(workspaceRoot))
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil
	}
	views := make([]codexWorkspaceView, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		if view, ok := readLocalClaudeSessionView(projectDir, entry.Name(), workspaceRoot); ok {
			views = append(views, view)
		}
	}
	return views
}

func readLocalClaudeSessionView(projectDir string, name string, workspaceRoot string) (codexWorkspaceView, bool) {
	path := filepath.Join(projectDir, name)
	summary := readLocalClaudeTranscriptSummary(path)
	info, err := os.Stat(path)
	if err != nil {
		return codexWorkspaceView{}, false
	}
	sessionID := strings.TrimSuffix(name, filepath.Ext(name))
	updatedAt := firstLocalCodexValue(summary.Timestamp, info.ModTime().UTC().Format(time.RFC3339))
	return codexWorkspaceView{
		WorkspaceRoot: workspaceRoot,
		ThreadID:      sessionID,
		ThreadName:    strings.TrimSpace(summary.Summary),
		UpdatedAt:     updatedAt,
		Source:        claudeLocalSource,
	}, sessionID != ""
}

func readLocalClaudeTranscriptSummary(path string) localClaudeTranscriptSummary {
	file, err := os.Open(path)
	if err != nil {
		return localClaudeTranscriptSummary{}
	}
	defer file.Close()
	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil && line == "" {
		return localClaudeTranscriptSummary{}
	}
	var summary localClaudeTranscriptSummary
	_ = json.Unmarshal([]byte(line), &summary)
	return summary
}

func encodeClaudeProjectPath(workspaceRoot string) string {
	workspaceRoot = normalizeClaudeWorkspaceRoot(workspaceRoot)
	return strings.ReplaceAll(workspaceRoot, string(filepath.Separator), "-")
}

func localClaudeWorkspaceExists(workspaceRoot string) bool {
	info, err := os.Stat(workspaceRoot)
	return err == nil && info.IsDir()
}
