package messaging

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type codexAppWorkspaceState struct {
	ProjectOrder        []string                   `json:"project-order"`
	SavedWorkspaceRoots []string                   `json:"electron-saved-workspace-roots"`
	LocalProjects       map[string]codexAppProject `json:"local-projects"`
}

type codexAppProject struct {
	Name      string   `json:"name"`
	RootPaths []string `json:"rootPaths"`
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
}

type codexAppWorkspace struct {
	Name string
	Root string
}

type codexAppThreadRow struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	RecencyAtMS  int64  `json:"recency_at_ms"`
	Source       string `json:"source"`
	ThreadSource string `json:"thread_source"`
}

// readCodexAppWorkspaces 读取 Codex App 侧真实保存的项目列表，作为远程窗口顶层空间来源。
func readCodexAppWorkspaces(codexDir string) []codexAppWorkspace {
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
	return mergeCodexAppWorkspaces(state.ProjectOrder, state.SavedWorkspaceRoots, state.LocalProjects)
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

func mergeCodexAppWorkspaces(projectOrder []string, savedRoots []string, projects map[string]codexAppProject) []codexAppWorkspace {
	seenRoots := map[string]bool{}
	seenProjects := map[string]bool{}
	workspaces := make([]codexAppWorkspace, 0, len(projectOrder)+len(savedRoots)+len(projects))
	appendWorkspace := func(name string, root string) {
		normalized := normalizeCodexWorkspaceRoot(root)
		if normalized == "" || seenRoots[normalized] || !localCodexWorkspaceExists(normalized) {
			return
		}
		seenRoots[normalized] = true
		name = strings.TrimSpace(name)
		if name == "" {
			name = shortCodexWorkspaceName(normalized)
		}
		workspaces = append(workspaces, codexAppWorkspace{Name: name, Root: normalized})
	}
	appendProject := func(project codexAppProject) {
		for _, root := range project.RootPaths {
			appendWorkspace(project.Name, root)
		}
	}

	for _, projectRef := range projectOrder {
		projectRef = strings.TrimSpace(projectRef)
		if project, ok := projects[projectRef]; ok {
			seenProjects[projectRef] = true
			appendProject(project)
			continue
		}
		appendWorkspace("", projectRef)
	}

	type unorderedProject struct {
		id      string
		project codexAppProject
	}
	unordered := make([]unorderedProject, 0, len(projects))
	for id, project := range projects {
		if !seenProjects[id] {
			unordered = append(unordered, unorderedProject{id: id, project: project})
		}
	}
	sort.SliceStable(unordered, func(i, j int) bool {
		if unordered[i].project.UpdatedAt != unordered[j].project.UpdatedAt {
			return unordered[i].project.UpdatedAt > unordered[j].project.UpdatedAt
		}
		if unordered[i].project.CreatedAt != unordered[j].project.CreatedAt {
			return unordered[i].project.CreatedAt > unordered[j].project.CreatedAt
		}
		if unordered[i].project.Name != unordered[j].project.Name {
			return unordered[i].project.Name < unordered[j].project.Name
		}
		return unordered[i].id < unordered[j].id
	})
	for _, entry := range unordered {
		appendProject(entry.project)
	}

	for _, root := range savedRoots {
		appendWorkspace("", root)
	}
	return workspaces
}
