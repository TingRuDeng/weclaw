package messaging

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const codexLocalSource = "local"

type localCodexIndexEntry struct {
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

type localCodexSessionMeta struct {
	ID         string
	Cwd        string
	Timestamp  string
	Originator string
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

	views := make([]codexWorkspaceView, 0, len(metas))
	for id, meta := range metas {
		entry := index[id]
		workspaceRoot := normalizeCodexWorkspaceRoot(meta.Cwd)
		if id == "" || workspaceRoot == "" {
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
			ID         string `json:"id"`
			Cwd        string `json:"cwd"`
			Timestamp  string `json:"timestamp"`
			Originator string `json:"originator"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &record); err != nil || record.Type != "session_meta" {
		return localCodexSessionMeta{}, false
	}
	meta := localCodexSessionMeta{
		ID:         strings.TrimSpace(record.Payload.ID),
		Cwd:        strings.TrimSpace(record.Payload.Cwd),
		Timestamp:  strings.TrimSpace(record.Payload.Timestamp),
		Originator: strings.TrimSpace(record.Payload.Originator),
	}
	return meta, meta.ID != "" && meta.Cwd != ""
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
