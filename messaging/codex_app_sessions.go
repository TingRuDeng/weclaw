package messaging

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type codexAppWorkspaceState struct {
	ProjectOrder        []string `json:"project-order"`
	SavedWorkspaceRoots []string `json:"electron-saved-workspace-roots"`
}

type codexAppThreadRow struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	RecencyAtMS  int64  `json:"recency_at_ms"`
	Source       string `json:"source"`
	ThreadSource string `json:"thread_source"`
}

// readCodexAppWorkspaceRoots 读取 Codex App 侧真实保存的项目列表，作为微信顶层空间来源。
func readCodexAppWorkspaceRoots(codexDir string) []string {
	codexDir = strings.TrimSpace(codexDir)
	if codexDir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(codexDir, ".codex-global-state.json"))
	if err != nil {
		return nil
	}
	var state codexAppWorkspaceState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return mergeCodexAppWorkspaceRoots(state.ProjectOrder, state.SavedWorkspaceRoots)
}

// readCodexAppWorkspaceThreads 读取 Codex App 当前项目内实际可见会话。
func readCodexAppWorkspaceThreads(codexDir string, workspaceRoot string) []codexWorkspaceView {
	codexDir = strings.TrimSpace(codexDir)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if codexDir == "" || workspaceRoot == "" {
		return nil
	}
	dbPath := filepath.Join(codexDir, "state_5.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	query := "select id, title, recency_at_ms, source, thread_source from threads where archived=0 and preview<>'' and cwd=" +
		sqliteString(workspaceRoot) + " and (thread_source is null or thread_source='' or thread_source='user') order by recency_at_ms desc, id desc"
	output, err := exec.Command("sqlite3", "-json", dbPath, query).Output()
	if err != nil {
		return nil
	}
	var rows []codexAppThreadRow
	if err := json.Unmarshal(output, &rows); err != nil {
		return nil
	}
	index := readLocalCodexSessionIndex(filepath.Join(codexDir, "session_index.jsonl"))
	views := make([]codexWorkspaceView, 0, len(rows))
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" {
			continue
		}
		if !isVisibleCodexAppThread(row) {
			continue
		}
		views = append(views, codexWorkspaceView{
			WorkspaceRoot: workspaceRoot,
			ThreadID:      id,
			ThreadName:    firstLocalCodexValue(index[id].ThreadName, firstCodexTitleLine(row.Title)),
			UpdatedAt:     strconvFormatInt(row.RecencyAtMS),
			Source:        codexLocalSource,
		})
	}
	return views
}

func isVisibleCodexAppThread(row codexAppThreadRow) bool {
	threadSource := strings.TrimSpace(row.ThreadSource)
	if threadSource != "" && threadSource != "user" {
		return false
	}
	return !localCodexSourceTextIsSubagent(row.Source)
}

func sqliteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func firstCodexTitleLine(title string) string {
	for _, line := range strings.Split(strings.TrimSpace(title), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func strconvFormatInt(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func mergeCodexAppWorkspaceRoots(projectOrder []string, savedRoots []string) []string {
	seen := map[string]bool{}
	roots := make([]string, 0, len(projectOrder)+len(savedRoots))
	for _, root := range append(projectOrder, savedRoots...) {
		normalized := normalizeCodexWorkspaceRoot(root)
		if normalized == "" || seen[normalized] || !localCodexWorkspaceExists(normalized) {
			continue
		}
		seen[normalized] = true
		roots = append(roots, normalized)
	}
	return roots
}
