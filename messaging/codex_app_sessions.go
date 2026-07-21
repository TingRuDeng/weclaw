package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type codexAppWorkspaceState struct {
	ProjectOrder             []string                                   `json:"project-order"`
	SavedWorkspaceRoots      []string                                   `json:"electron-saved-workspace-roots"`
	LocalProjects            map[string]codexAppProject                 `json:"local-projects"`
	ThreadProjectAssignments map[string]codexAppThreadProjectAssignment `json:"thread-project-assignments"`
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

type codexAppThreadProjectAssignment struct {
	ProjectKind string `json:"projectKind"`
	ProjectID   string `json:"projectId"`
}

type codexAppThreadRow struct {
	ID           string `json:"id"`
	Cwd          string `json:"cwd"`
	Title        string `json:"title"`
	RecencyAtMS  int64  `json:"recency_at_ms"`
	Source       string `json:"source"`
	ThreadSource string `json:"thread_source"`
}

// readCodexAppWorkspaces 读取 Codex App 侧真实保存的项目列表，作为远程窗口顶层空间来源。
func readCodexAppWorkspaces(codexDir string) ([]codexAppWorkspace, bool, error) {
	codexDir = strings.TrimSpace(codexDir)
	if codexDir == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(filepath.Join(codexDir, ".codex-global-state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("read Codex App workspace state: %w", err)
	}
	var state codexAppWorkspaceState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, true, fmt.Errorf("parse Codex App workspace state: %w", err)
	}
	projectRecency, err := readCodexAppProjectRecency(codexDir, state)
	if err != nil {
		return nil, true, err
	}
	return mergeCodexAppWorkspaces(state, projectRecency), true, nil
}

// readCodexAppProjectRecency 还原 Codex App 顶层项目排序使用的最近会话时间。
func readCodexAppProjectRecency(codexDir string, state codexAppWorkspaceState) (map[string]int64, error) {
	dbPath := filepath.Join(codexDir, "state_5.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect Codex App thread database: %w", err)
	}
	query := "select id, cwd, recency_at_ms, source, thread_source from threads where archived=0 and preview<>'' and " +
		"(thread_source is null or thread_source='' or thread_source='user')"
	output, err := exec.Command("sqlite3", "-json", dbPath, query).Output()
	if err != nil {
		return nil, fmt.Errorf("query Codex App project recency: %w", err)
	}
	var rows []codexAppThreadRow
	if len(strings.TrimSpace(string(output))) > 0 {
		if err := json.Unmarshal(output, &rows); err != nil {
			return nil, fmt.Errorf("parse Codex App project recency: %w", err)
		}
	}

	projectByRoot := make(map[string]string, len(state.LocalProjects))
	for id, project := range state.LocalProjects {
		for _, root := range project.RootPaths {
			normalized := normalizeCodexWorkspaceRoot(root)
			if normalized != "" {
				projectByRoot[normalized] = id
			}
		}
	}
	projectRecency := make(map[string]int64, len(state.LocalProjects))
	for _, row := range rows {
		if !isVisibleCodexAppThread(row) {
			continue
		}
		projectID := ""
		if assignment, ok := state.ThreadProjectAssignments[strings.TrimSpace(row.ID)]; ok &&
			strings.TrimSpace(assignment.ProjectKind) == "local" {
			candidate := strings.TrimSpace(assignment.ProjectID)
			if _, exists := state.LocalProjects[candidate]; exists {
				projectID = candidate
			}
		}
		if projectID == "" {
			projectID = projectByRoot[normalizeCodexWorkspaceRoot(row.Cwd)]
		}
		if projectID != "" && row.RecencyAtMS > projectRecency[projectID] {
			projectRecency[projectID] = row.RecencyAtMS
		}
	}
	return projectRecency, nil
}

// readCodexAppWorkspaceThreads 读取 Codex App 当前项目内实际可见会话。
func readCodexAppWorkspaceThreads(codexDir string, workspaceRoot string) ([]codexWorkspaceView, bool, error) {
	codexDir = strings.TrimSpace(codexDir)
	workspaceRoot = normalizeCodexWorkspaceRoot(workspaceRoot)
	if codexDir == "" || workspaceRoot == "" {
		return nil, false, nil
	}
	dbPath := filepath.Join(codexDir, "state_5.sqlite")
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, true, fmt.Errorf("inspect Codex App thread database: %w", err)
	}
	query := "select id, title, recency_at_ms, source, thread_source from threads where archived=0 and preview<>'' and cwd=" +
		sqliteString(workspaceRoot) + " and (thread_source is null or thread_source='' or thread_source='user') order by recency_at_ms desc, id desc"
	output, err := exec.Command("sqlite3", "-json", dbPath, query).Output()
	if err != nil {
		return nil, true, fmt.Errorf("query Codex App workspace threads: %w", err)
	}
	var rows []codexAppThreadRow
	if len(strings.TrimSpace(string(output))) > 0 {
		if err := json.Unmarshal(output, &rows); err != nil {
			return nil, true, fmt.Errorf("parse Codex App workspace threads: %w", err)
		}
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
	return views, true, nil
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

func mergeCodexAppWorkspaces(state codexAppWorkspaceState, projectRecency map[string]int64) []codexAppWorkspace {
	seenRoots := map[string]bool{}
	workspaces := make([]codexAppWorkspace, 0, len(state.ProjectOrder)+len(state.SavedWorkspaceRoots)+len(state.LocalProjects))
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

	type projectEntry struct {
		id      string
		project codexAppProject
		recency int64
	}
	projects := make([]projectEntry, 0, len(state.LocalProjects))
	for id, project := range state.LocalProjects {
		recency := project.UpdatedAt
		if projectRecency[id] > recency {
			recency = projectRecency[id]
		}
		projects = append(projects, projectEntry{id: id, project: project, recency: recency})
	}
	sort.SliceStable(projects, func(i, j int) bool {
		if projects[i].recency != projects[j].recency {
			return projects[i].recency > projects[j].recency
		}
		if projects[i].project.CreatedAt != projects[j].project.CreatedAt {
			return projects[i].project.CreatedAt > projects[j].project.CreatedAt
		}
		if projects[i].project.Name != projects[j].project.Name {
			return projects[i].project.Name < projects[j].project.Name
		}
		return projects[i].id < projects[j].id
	})

	byID := make(map[string]codexAppProject, len(projects))
	for _, entry := range projects {
		byID[entry.id] = entry.project
	}
	appendedProjects := make(map[string]bool, len(projects))
	appendProjectID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || appendedProjects[id] {
			return
		}
		project, ok := byID[id]
		if !ok {
			return
		}
		appendedProjects[id] = true
		appendProject(project)
	}

	orderedProjects := make(map[string]bool, len(state.ProjectOrder))
	legacyProjectRefs := make([]string, 0)
	for _, projectRef := range state.ProjectOrder {
		projectRef = strings.TrimSpace(projectRef)
		if _, ok := byID[projectRef]; ok {
			orderedProjects[projectRef] = true
		} else if projectRef != "" {
			legacyProjectRefs = append(legacyProjectRefs, projectRef)
		}
	}

	// Codex App 先按项目活动时间排序，再把 project-order 中的项目移到末尾并保持显式顺序。
	// 新建但尚未写入 project-order 的项目因此会显示在已有项目之前。
	for _, entry := range projects {
		if !orderedProjects[entry.id] {
			appendProjectID(entry.id)
		}
	}
	for _, id := range state.ProjectOrder {
		appendProjectID(id)
	}
	for _, projectRef := range legacyProjectRefs {
		appendWorkspace("", projectRef)
	}

	for _, root := range state.SavedWorkspaceRoots {
		appendWorkspace("", root)
	}
	return workspaces
}
