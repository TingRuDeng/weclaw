package messaging

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const codexLocalSource = "local"

type localCodexIndexEntry struct {
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

type localCodexSessionMeta struct {
	ID           string
	Cwd          string
	Timestamp    string
	Originator   string
	ThreadSource string
	Source       json.RawMessage
}

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

// defaultCodexLocalSessionDir 返回本机 Codex 默认会话目录。
func defaultCodexLocalSessionDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

// discoverLocalCodexSessions 只读取本机 Codex 会话元数据，避免把历史对话正文暴露到微信。
func discoverLocalCodexSessions(codexDir string) []codexWorkspaceView {
	codexDir = strings.TrimSpace(codexDir)
	if codexDir == "" {
		return nil
	}
	index := readLocalCodexSessionIndex(filepath.Join(codexDir, "session_index.jsonl"))
	metas := readLocalCodexSessionMetas(filepath.Join(codexDir, "sessions"))
	archivedIDs := readLocalCodexArchivedSessionIDs(filepath.Join(codexDir, "archived_sessions"))

	views := make([]codexWorkspaceView, 0, len(metas))
	for id, meta := range metas {
		if archivedIDs[id] {
			continue
		}
		if !isVisibleLocalCodexSession(meta) {
			continue
		}
		entry := index[id]
		workspaceRoot := normalizeCodexWorkspaceRoot(meta.Cwd)
		if id == "" || workspaceRoot == "" {
			continue
		}
		if !localCodexWorkspaceExists(workspaceRoot) {
			continue
		}
		views = append(views, codexWorkspaceView{
			WorkspaceRoot: workspaceRoot,
			ThreadID:      id,
			ThreadName:    entry.ThreadName,
			UpdatedAt:     firstLocalCodexValue(entry.UpdatedAt, meta.Timestamp),
			Source:        codexLocalSource,
		})
	}
	sortLocalCodexSessions(views)
	return views
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

func localCodexWorkspaceExists(workspaceRoot string) bool {
	info, err := os.Stat(workspaceRoot)
	return err == nil && info.IsDir()
}

// readLocalCodexSessionIndex 读取 Codex 索引文件里的 thread 名称和更新时间。
func readLocalCodexSessionIndex(indexPath string) map[string]localCodexIndexEntry {
	file, err := os.Open(indexPath)
	if err != nil {
		return map[string]localCodexIndexEntry{}
	}
	defer file.Close()

	entries := map[string]localCodexIndexEntry{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		id, entry, ok := parseLocalCodexIndexLine(scanner.Bytes())
		if ok {
			entries[id] = entry
		}
	}
	return entries
}

// parseLocalCodexIndexLine 解析单行索引记录，异常行直接跳过。
func parseLocalCodexIndexLine(line []byte) (string, localCodexIndexEntry, bool) {
	var record struct {
		ID string `json:"id"`
		localCodexIndexEntry
	}
	if err := json.Unmarshal(line, &record); err != nil {
		return "", localCodexIndexEntry{}, false
	}
	id := strings.TrimSpace(record.ID)
	return id, record.localCodexIndexEntry, id != ""
}

// readLocalCodexSessionMetas 扫描 sessions 目录，只保留每个 thread 的 session_meta。
func readLocalCodexSessionMetas(root string) map[string]localCodexSessionMeta {
	metas := map[string]localCodexSessionMeta{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		meta, ok := readLocalCodexSessionMeta(path)
		if ok {
			metas[meta.ID] = meta
		}
		return nil
	})
	return metas
}

// readLocalCodexArchivedSessionIDs 读取已归档 thread，避免微信列表重新展示归档会话。
func readLocalCodexArchivedSessionIDs(root string) map[string]bool {
	ids := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if meta, ok := readLocalCodexSessionMeta(path); ok {
			ids[meta.ID] = true
			return nil
		}
		if id := localCodexThreadIDFromPath(path); id != "" {
			ids[id] = true
		}
		return nil
	})
	return ids
}

// localCodexThreadIDFromPath 从归档文件名兜底提取 thread id。
func localCodexThreadIDFromPath(path string) string {
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.TrimPrefix(name, "rollout-")
	return strings.TrimSpace(name)
}

// readLocalCodexSessionMeta 只读取 jsonl 首行，避免把完整对话内容加载进内存。
func readLocalCodexSessionMeta(path string) (localCodexSessionMeta, bool) {
	file, err := os.Open(path)
	if err != nil {
		log.Printf("[codex-session] failed to open local session %s: %v", path, err)
		return localCodexSessionMeta{}, false
	}
	defer file.Close()

	line, err := bufio.NewReader(file).ReadString('\n')
	if err != nil && err != io.EOF {
		return localCodexSessionMeta{}, false
	}
	return parseLocalCodexSessionMeta([]byte(line))
}

// parseLocalCodexSessionMeta 从 session_meta 中提取恢复 thread 所需的最小字段。
func parseLocalCodexSessionMeta(line []byte) (localCodexSessionMeta, bool) {
	var record struct {
		Type    string `json:"type"`
		Payload struct {
			ID           string          `json:"id"`
			Cwd          string          `json:"cwd"`
			Timestamp    string          `json:"timestamp"`
			Originator   string          `json:"originator"`
			ThreadSource string          `json:"thread_source"`
			Source       json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &record); err != nil || record.Type != "session_meta" {
		return localCodexSessionMeta{}, false
	}
	meta := localCodexSessionMeta{
		ID:           strings.TrimSpace(record.Payload.ID),
		Cwd:          strings.TrimSpace(record.Payload.Cwd),
		Timestamp:    strings.TrimSpace(record.Payload.Timestamp),
		Originator:   strings.TrimSpace(record.Payload.Originator),
		ThreadSource: strings.TrimSpace(record.Payload.ThreadSource),
		Source:       record.Payload.Source,
	}
	return meta, meta.ID != "" && meta.Cwd != ""
}

// isVisibleLocalCodexSession 保持本机扫描结果接近 Codex 桌面端可见的用户主会话。
func isVisibleLocalCodexSession(meta localCodexSessionMeta) bool {
	if strings.TrimSpace(meta.Originator) != "Codex Desktop" {
		return false
	}
	threadSource := strings.TrimSpace(meta.ThreadSource)
	if threadSource != "" && threadSource != "user" {
		return false
	}
	return !localCodexSourceIsSubagent(meta.Source)
}

func localCodexSourceIsSubagent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	return localCodexSourceTextIsSubagent(string(raw))
}

func localCodexSourceTextIsSubagent(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if unquoted, err := strconv.Unquote(raw); err == nil {
		raw = strings.TrimSpace(unquoted)
	}
	var source struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if err := json.Unmarshal([]byte(raw), &source); err != nil {
		return false
	}
	return len(source.Subagent) > 0
}

// sortLocalCodexSessions 按更新时间倒序排列，便于微信里优先看到最近会话。
func sortLocalCodexSessions(views []codexWorkspaceView) {
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].UpdatedAt != views[j].UpdatedAt {
			return views[i].UpdatedAt > views[j].UpdatedAt
		}
		return views[i].ThreadID < views[j].ThreadID
	})
}

// firstLocalCodexValue 返回第一个非空元数据值。
func firstLocalCodexValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
