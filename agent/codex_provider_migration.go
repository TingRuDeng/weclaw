package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

var codexProviderTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var codexThreadIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var codexCatalogHostIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

var codexResponseItemPrefixes = map[string]string{
	"additional_tools":        "at",
	"agent_message":           "amsg",
	"compaction":              "cmp",
	"context_compaction":      "cmp",
	"custom_tool_call":        "ctc",
	"custom_tool_call_output": "ctco",
	"function_call":           "fc",
	"function_call_output":    "fco",
	"image_generation_call":   "ig",
	"local_shell_call":        "lsh",
	"message":                 "msg",
	"reasoning":               "rs",
	"tool_search_call":        "tsc",
	"tool_search_output":      "tso",
	"web_search_call":         "ws",
}

type codexProviderMigrationRequest struct {
	CodexHome      string
	ThreadID       string
	TargetProvider string
}

type codexProviderMigrationResult struct {
	Changed          bool
	PreviousProvider string
	TargetProvider   string
	RolloutPath      string
	BackupDir        string
	Transform        codexRolloutProviderTransformResult
}

type codexRolloutProviderTransformResult struct {
	DeletedProviderBoundItems int `json:"deletedProviderBoundItems"`
	RepairedItemIDs           int `json:"repairedItemIds"`
}

type codexProviderStateRow struct {
	ID            string `json:"id"`
	RolloutPath   string `json:"rollout_path"`
	ModelProvider string `json:"model_provider"`
}

type codexProviderCatalogRow struct {
	HostID        string `json:"host_id"`
	ModelProvider string `json:"model_provider"`
}

type codexProviderMigrationManifest struct {
	Version          int                                 `json:"version"`
	Status           string                              `json:"status"`
	ThreadID         string                              `json:"threadId"`
	RolloutPath      string                              `json:"rolloutPath"`
	OriginalSHA256   string                              `json:"originalSha256"`
	PreviousProvider string                              `json:"previousProvider"`
	TargetProvider   string                              `json:"targetProvider"`
	CatalogRows      []codexProviderCatalogRow           `json:"catalogRows,omitempty"`
	Transform        codexRolloutProviderTransformResult `json:"transform"`
	UpdatedAt        time.Time                           `json:"updatedAt"`
}

func migrateCodexThreadProvider(ctx context.Context, req codexProviderMigrationRequest) (codexProviderMigrationResult, error) {
	req.CodexHome = filepath.Clean(strings.TrimSpace(req.CodexHome))
	req.ThreadID = strings.TrimSpace(req.ThreadID)
	req.TargetProvider = strings.TrimSpace(req.TargetProvider)
	if !filepath.IsAbs(req.CodexHome) {
		return codexProviderMigrationResult{}, fmt.Errorf("CODEX_HOME 必须是绝对路径")
	}
	if !codexThreadIDPattern.MatchString(req.ThreadID) {
		return codexProviderMigrationResult{}, fmt.Errorf("Codex thread ID 含有不安全字符")
	}
	if !codexProviderTokenPattern.MatchString(req.TargetProvider) {
		return codexProviderMigrationResult{}, fmt.Errorf("Codex provider %q 无效", req.TargetProvider)
	}
	if err := validateCodexProviderHome(req.CodexHome); err != nil {
		return codexProviderMigrationResult{}, err
	}

	stateDB := filepath.Join(req.CodexHome, "state_5.sqlite")
	if err := validateCodexProviderFile(stateDB); err != nil {
		return codexProviderMigrationResult{}, fmt.Errorf("验证 Codex thread 数据库: %w", err)
	}
	stateRow, err := readCodexProviderStateRow(ctx, stateDB, req.ThreadID)
	if err != nil {
		return codexProviderMigrationResult{}, err
	}
	result := codexProviderMigrationResult{
		PreviousProvider: stateRow.ModelProvider,
		TargetProvider:   req.TargetProvider,
		RolloutPath:      stateRow.RolloutPath,
	}
	if stateRow.ModelProvider == req.TargetProvider {
		return result, nil
	}
	if !codexProviderTokenPattern.MatchString(stateRow.ModelProvider) {
		return result, fmt.Errorf("历史 provider %q 无法安全迁移", stateRow.ModelProvider)
	}
	if err := validateCodexProviderRolloutPath(req.CodexHome, stateRow.RolloutPath); err != nil {
		return result, err
	}
	original, err := os.ReadFile(stateRow.RolloutPath)
	if err != nil {
		return result, fmt.Errorf("读取 Codex rollout: %w", err)
	}
	transformed, transformResult, err := transformCodexRolloutProvider(original, req.ThreadID, req.TargetProvider)
	if err != nil {
		return result, err
	}
	result.Transform = transformResult

	catalogDB := filepath.Join(req.CodexHome, "sqlite", "codex-dev.db")
	catalogRows, catalogAvailable, err := readCodexProviderCatalogRows(ctx, catalogDB, req.ThreadID)
	if err != nil {
		return result, err
	}
	for _, row := range catalogRows {
		if !codexCatalogHostIDPattern.MatchString(row.HostID) || !codexProviderTokenPattern.MatchString(row.ModelProvider) {
			return result, fmt.Errorf("Codex catalog 含有无法安全迁移的 provider 元数据")
		}
	}

	backupDir, err := createCodexProviderBackupDir(req.CodexHome, req.ThreadID)
	if err != nil {
		return result, err
	}
	result.BackupDir = backupDir
	manifest := codexProviderMigrationManifest{
		Version: 1, Status: "prepared", ThreadID: req.ThreadID,
		RolloutPath: stateRow.RolloutPath, OriginalSHA256: sha256Hex(original),
		PreviousProvider: stateRow.ModelProvider, TargetProvider: req.TargetProvider,
		CatalogRows: catalogRows, Transform: transformResult, UpdatedAt: time.Now().UTC(),
	}
	if err := securefile.Write(filepath.Join(backupDir, "rollout.jsonl"), original); err != nil {
		return result, fmt.Errorf("备份 Codex rollout: %w", err)
	}
	if err := writeCodexProviderManifest(backupDir, &manifest, "prepared"); err != nil {
		return result, err
	}

	rolloutWritten := false
	stateUpdated := false
	catalogUpdated := false
	rollback := func(cause error) error {
		var rollbackErrs []error
		if catalogUpdated {
			rollbackErrs = append(rollbackErrs, restoreCodexProviderCatalogRows(ctx, catalogDB, req.ThreadID, catalogRows))
		}
		if stateUpdated {
			rollbackErrs = append(rollbackErrs, updateCodexProviderStateRow(ctx, stateDB, req.ThreadID, stateRow.ModelProvider))
		}
		if rolloutWritten {
			rollbackErrs = append(rollbackErrs, writeCodexProviderFileAtomically(stateRow.RolloutPath, original))
		}
		rollbackErr := errors.Join(rollbackErrs...)
		status := "rolled_back"
		if rollbackErr != nil {
			status = "rollback_failed"
		}
		manifestErr := writeCodexProviderManifest(backupDir, &manifest, status)
		if rollbackErr != nil || manifestErr != nil {
			return fmt.Errorf("%w；provider 迁移回滚失败: %v", cause, errors.Join(rollbackErr, manifestErr))
		}
		return cause
	}

	if err := writeCodexProviderFileAtomically(stateRow.RolloutPath, transformed); err != nil {
		return result, rollback(fmt.Errorf("写入迁移后的 Codex rollout: %w", err))
	}
	rolloutWritten = true
	if err := writeCodexProviderManifest(backupDir, &manifest, "rollout_written"); err != nil {
		return result, rollback(err)
	}
	if err := updateCodexProviderStateRow(ctx, stateDB, req.ThreadID, req.TargetProvider); err != nil {
		return result, rollback(err)
	}
	stateUpdated = true
	if err := writeCodexProviderManifest(backupDir, &manifest, "state_updated"); err != nil {
		return result, rollback(err)
	}
	if catalogAvailable {
		if err := updateCodexProviderCatalogRows(ctx, catalogDB, req.ThreadID, req.TargetProvider); err != nil {
			return result, rollback(err)
		}
		catalogUpdated = true
	}
	if err := writeCodexProviderManifest(backupDir, &manifest, "storage_committed"); err != nil {
		return result, rollback(fmt.Errorf("记录 provider 迁移提交状态: %w", err))
	}
	result.Changed = true
	return result, nil
}

func transformCodexRolloutProvider(original []byte, threadID string, targetProvider string) ([]byte, codexRolloutProviderTransformResult, error) {
	if !codexThreadIDPattern.MatchString(strings.TrimSpace(threadID)) || !codexProviderTokenPattern.MatchString(strings.TrimSpace(targetProvider)) {
		return nil, codexRolloutProviderTransformResult{}, fmt.Errorf("Codex rollout 迁移参数无效")
	}
	type rolloutRecord map[string]any
	records := make([]rolloutRecord, 0, bytes.Count(original, []byte{'\n'})+1)
	scanner := bufio.NewScanner(bytes.NewReader(original))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 16*1024*1024)
	deletedIDs := make(map[string]bool)
	metaFound := false
	result := codexRolloutProviderTransformResult{}
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record rolloutRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, result, fmt.Errorf("Codex rollout 第 %d 行不是有效 JSON: %w", lineNumber, err)
		}
		payload, _ := record["payload"].(map[string]any)
		switch record["type"] {
		case "session_meta":
			if payload == nil || strings.TrimSpace(anyString(payload["id"])) != threadID {
				return nil, result, fmt.Errorf("Codex rollout session_meta 与目标 thread 不一致")
			}
			payload["model_provider"] = targetProvider
			metaFound = true
		case "response_item":
			if payload == nil {
				return nil, result, fmt.Errorf("Codex rollout response_item 缺少 payload")
			}
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, result, fmt.Errorf("读取 Codex rollout: %w", err)
	}
	if !metaFound {
		return nil, result, fmt.Errorf("Codex rollout 缺少目标 thread 的 session_meta")
	}

	sanitizedRecords := records[:0]
	for _, record := range records {
		payload, _ := record["payload"].(map[string]any)
		if payload != nil && (record["type"] == "response_item" || record["type"] == "compacted") {
			sanitized, keep, sanitizeErr := sanitizeCodexProviderBoundItems(payload, deletedIDs, &result)
			if sanitizeErr != nil {
				return nil, result, sanitizeErr
			}
			if !keep {
				if record["type"] != "response_item" {
					return nil, result, fmt.Errorf("Codex rollout compacted payload 无法安全清理")
				}
				continue
			}
			record["payload"] = sanitized
		}
		sanitizedRecords = append(sanitizedRecords, record)
	}
	records = sanitizedRecords

	existingIDs := make(map[string]bool)
	replacements := make(map[string]string)
	for _, record := range records {
		if err := collectCodexItemIDReplacements(map[string]any(record), existingIDs, replacements); err != nil {
			return nil, result, err
		}
	}
	for originalID, replacement := range replacements {
		if existingIDs[replacement] && replacement != originalID {
			return nil, result, fmt.Errorf("Codex rollout item ID 修复发生冲突: %s", replacement)
		}
	}
	result.RepairedItemIDs = len(replacements)

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		payload, _ := record["payload"].(map[string]any)
		if record["type"] == "response_item" && deletedIDs[strings.TrimSpace(anyString(payload["id"]))] {
			continue
		}
		if record["type"] == "response_item_reference" && payload != nil && deletedIDs[strings.TrimSpace(anyString(payload["item_id"]))] {
			continue
		}
		if referencesDeletedCodexItem(map[string]any(record), deletedIDs, "") {
			return nil, result, fmt.Errorf("Codex rollout 仍引用已删除的 provider reasoning item")
		}
		record = rolloutRecord(replaceCodexItemReferences(map[string]any(record), replacements, "").(map[string]any))
		payload, _ = record["payload"].(map[string]any)
		if record["type"] == "response_item" && payload != nil {
			if replacement := replacements[strings.TrimSpace(anyString(payload["id"]))]; replacement != "" {
				payload["id"] = replacement
			}
		}
		if containsCodexEncryptedContent(map[string]any(record)) {
			return nil, result, fmt.Errorf("Codex rollout 迁移后仍含 encrypted_content")
		}
		if err := encoder.Encode(record); err != nil {
			return nil, result, fmt.Errorf("编码 Codex rollout: %w", err)
		}
	}
	return output.Bytes(), result, nil
}

func containsCodexEncryptedContent(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "encrypted_content" {
				if text, ok := child.(string); !ok || strings.TrimSpace(text) != "" {
					return true
				}
			}
			if containsCodexEncryptedContent(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsCodexEncryptedContent(child) {
				return true
			}
		}
	}
	return false
}

func sanitizeCodexProviderBoundItems(
	value any,
	deleted map[string]bool,
	result *codexRolloutProviderTransformResult,
) (any, bool, error) {
	switch typed := value.(type) {
	case map[string]any:
		if encrypted, exists := typed["encrypted_content"]; exists {
			if text, ok := encrypted.(string); !ok || strings.TrimSpace(text) != "" {
				itemType := strings.TrimSpace(anyString(typed["type"]))
				switch itemType {
				case "reasoning", "compaction", "context_compaction":
					if itemID := strings.TrimSpace(anyString(typed["id"])); itemID != "" {
						deleted[itemID] = true
					}
					result.DeletedProviderBoundItems++
					return nil, false, nil
				default:
					return nil, false, fmt.Errorf("Codex rollout 的 %q item 含无法安全迁移的 encrypted_content", itemType)
				}
			}
		}
		for key, child := range typed {
			sanitized, keep, err := sanitizeCodexProviderBoundItems(child, deleted, result)
			if err != nil {
				return nil, false, err
			}
			if !keep {
				delete(typed, key)
				continue
			}
			typed[key] = sanitized
		}
		return typed, true, nil
	case []any:
		sanitized := make([]any, 0, len(typed))
		for _, child := range typed {
			item, keep, err := sanitizeCodexProviderBoundItems(child, deleted, result)
			if err != nil {
				return nil, false, err
			}
			if keep {
				sanitized = append(sanitized, item)
			}
		}
		return sanitized, true, nil
	default:
		return value, true, nil
	}
}

func collectCodexItemIDReplacements(value any, existing map[string]bool, replacements map[string]string) error {
	switch typed := value.(type) {
	case map[string]any:
		itemType := strings.TrimSpace(anyString(typed["type"]))
		if prefix := codexResponseItemPrefixes[itemType]; prefix != "" {
			itemID := strings.TrimSpace(anyString(typed["id"]))
			if itemID != "" {
				existing[itemID] = true
			}
			if strings.HasPrefix(itemID, "item_") {
				replacement := prefix + "_" + strings.TrimPrefix(itemID, "item_")
				if previous := replacements[itemID]; previous != "" && previous != replacement {
					return fmt.Errorf("Codex rollout item %q 对应多个 response 类型", itemID)
				}
				replacements[itemID] = replacement
			}
		}
		for _, child := range typed {
			if err := collectCodexItemIDReplacements(child, existing, replacements); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := collectCodexItemIDReplacements(child, existing, replacements); err != nil {
				return err
			}
		}
	}
	return nil
}

func referencesDeletedCodexItem(value any, deleted map[string]bool, key string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if referencesDeletedCodexItem(child, deleted, childKey) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if referencesDeletedCodexItem(child, deleted, key) {
				return true
			}
		}
	case string:
		return (key == "item_id" || strings.HasSuffix(key, "item_id")) && deleted[typed]
	}
	return false
}

func replaceCodexItemReferences(value any, replacements map[string]string, key string) any {
	switch typed := value.(type) {
	case map[string]any:
		if codexResponseItemPrefixes[strings.TrimSpace(anyString(typed["type"]))] != "" {
			if itemID := strings.TrimSpace(anyString(typed["id"])); replacements[itemID] != "" {
				typed["id"] = replacements[itemID]
			}
		}
		for childKey, child := range typed {
			if text, ok := child.(string); ok && (childKey == "item_id" || strings.HasSuffix(childKey, "item_id")) {
				if replacement := replacements[text]; replacement != "" {
					typed[childKey] = replacement
				}
				continue
			}
			typed[childKey] = replaceCodexItemReferences(child, replacements, childKey)
		}
	case []any:
		for index, child := range typed {
			typed[index] = replaceCodexItemReferences(child, replacements, key)
		}
	}
	return value
}

func readCodexProviderStateRow(ctx context.Context, database string, threadID string) (codexProviderStateRow, error) {
	var rows []codexProviderStateRow
	if err := runCodexProviderSQLiteJSON(ctx, database, map[string]string{"@thread": threadID},
		"SELECT id, rollout_path, model_provider FROM threads WHERE id=@thread;", &rows); err != nil {
		return codexProviderStateRow{}, fmt.Errorf("读取 Codex thread provider: %w", err)
	}
	if len(rows) != 1 || strings.TrimSpace(rows[0].ID) != threadID {
		return codexProviderStateRow{}, fmt.Errorf("Codex thread %q 不存在或不唯一", threadID)
	}
	return rows[0], nil
}

func readCodexProviderCatalogRows(ctx context.Context, database string, threadID string) ([]codexProviderCatalogRow, bool, error) {
	if _, err := os.Lstat(database); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, fmt.Errorf("检查 Codex local catalog: %w", err)
	}
	if err := validateCodexProviderFile(database); err != nil {
		return nil, false, fmt.Errorf("验证 Codex local catalog: %w", err)
	}
	var schema []struct {
		Name string `json:"name"`
	}
	if err := runCodexProviderSQLiteJSON(ctx, database, nil,
		"SELECT name FROM sqlite_master WHERE type='table' AND name='local_thread_catalog';", &schema); err != nil {
		return nil, false, err
	}
	if len(schema) == 0 {
		return nil, false, nil
	}
	var rows []codexProviderCatalogRow
	if err := runCodexProviderSQLiteJSON(ctx, database, map[string]string{"@thread": threadID},
		"SELECT host_id, model_provider FROM local_thread_catalog WHERE thread_id=@thread ORDER BY host_id;", &rows); err != nil {
		return nil, false, fmt.Errorf("读取 Codex local catalog provider: %w", err)
	}
	return rows, true, nil
}

func updateCodexProviderStateRow(ctx context.Context, database string, threadID string, provider string) error {
	output, err := runCodexProviderSQLiteOutput(ctx, database, map[string]string{"@thread": threadID, "@provider": provider},
		"BEGIN IMMEDIATE; UPDATE threads SET model_provider=@provider WHERE id=@thread; SELECT changes(); COMMIT;")
	if err != nil {
		return err
	}
	if strings.TrimSpace(output) != "1" {
		return fmt.Errorf("Codex thread provider 更新行数不是 1")
	}
	return nil
}

func updateCodexProviderCatalogRows(ctx context.Context, database string, threadID string, provider string) error {
	return runCodexProviderSQLite(ctx, database, map[string]string{"@thread": threadID, "@provider": provider},
		"BEGIN IMMEDIATE; UPDATE local_thread_catalog SET model_provider=@provider WHERE thread_id=@thread; COMMIT;")
}

func restoreCodexProviderCatalogRows(ctx context.Context, database string, threadID string, rows []codexProviderCatalogRow) error {
	var restoreErrs []error
	for _, row := range rows {
		restoreErrs = append(restoreErrs, runCodexProviderSQLite(ctx, database, map[string]string{
			"@thread": threadID, "@host": row.HostID, "@provider": row.ModelProvider,
		}, "BEGIN IMMEDIATE; UPDATE local_thread_catalog SET model_provider=@provider WHERE thread_id=@thread AND host_id=@host; COMMIT;"))
	}
	return errors.Join(restoreErrs...)
}

func runCodexProviderSQLiteJSON(ctx context.Context, database string, parameters map[string]string, query string, destination any) error {
	args := codexProviderSQLiteArgs(true, parameters, database, query)
	output, err := exec.CommandContext(ctx, "sqlite3", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sqlite3: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(bytes.TrimSpace(output)) == 0 {
		output = []byte("[]")
	}
	if err := json.Unmarshal(output, destination); err != nil {
		return fmt.Errorf("解析 sqlite3 JSON: %w", err)
	}
	return nil
}

func runCodexProviderSQLite(ctx context.Context, database string, parameters map[string]string, query string) error {
	_, err := runCodexProviderSQLiteOutput(ctx, database, parameters, query)
	return err
}

func runCodexProviderSQLiteOutput(ctx context.Context, database string, parameters map[string]string, query string) (string, error) {
	args := codexProviderSQLiteArgs(false, parameters, database, query)
	output, err := exec.CommandContext(ctx, "sqlite3", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("sqlite3: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func codexProviderSQLiteArgs(jsonOutput bool, parameters map[string]string, database string, query string) []string {
	args := make([]string, 0, 6+len(parameters)*2)
	if jsonOutput {
		args = append(args, "-json")
	}
	args = append(args, "-cmd", ".parameter init")
	for _, name := range []string{"@thread", "@host", "@provider"} {
		if value, ok := parameters[name]; ok {
			args = append(args, "-cmd", ".parameter set "+name+" "+value)
		}
	}
	args = append(args, database, query)
	return args
}

func createCodexProviderBackupDir(codexHome string, threadID string) (string, error) {
	root := filepath.Join(codexHome, "backups", "weclaw-provider-migration")
	if err := securefile.EnsureDir(root); err != nil {
		return "", fmt.Errorf("创建 provider 迁移备份目录: %w", err)
	}
	shortID := threadID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	backupDir, err := os.MkdirTemp(root, time.Now().UTC().Format("20060102T150405Z")+"-"+shortID+"-")
	if err != nil {
		return "", fmt.Errorf("创建 provider 迁移备份: %w", err)
	}
	if err := os.Chmod(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("保护 provider 迁移备份: %w", err)
	}
	return backupDir, nil
}

func writeCodexProviderManifest(backupDir string, manifest *codexProviderMigrationManifest, status string) error {
	manifest.Status = status
	manifest.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("编码 provider 迁移记录: %w", err)
	}
	if err := securefile.Write(filepath.Join(backupDir, "manifest.json"), append(data, '\n')); err != nil {
		return fmt.Errorf("写入 provider 迁移记录: %w", err)
	}
	return nil
}

func markCodexProviderMigrationVerified(backupDir string) error {
	manifestPath := filepath.Join(backupDir, "manifest.json")
	data, err := securefile.Read(manifestPath)
	if err != nil {
		return fmt.Errorf("读取 provider 迁移记录: %w", err)
	}
	var manifest codexProviderMigrationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("解析 provider 迁移记录: %w", err)
	}
	if manifest.Status != "storage_committed" {
		return fmt.Errorf("provider 迁移记录状态 %q 无法标记为 verified", manifest.Status)
	}
	return writeCodexProviderManifest(backupDir, &manifest, "verified")
}

func validateCodexProviderHome(codexHome string) error {
	info, err := os.Lstat(codexHome)
	if err != nil {
		return fmt.Errorf("检查 CODEX_HOME: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("CODEX_HOME 必须是实体目录")
	}
	return validateCodexProviderOwner(info)
}

func validateCodexProviderFile(filename string) error {
	info, err := os.Lstat(filename)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("路径必须是实体普通文件: %s", filename)
	}
	return validateCodexProviderOwner(info)
}

func validateCodexProviderRolloutPath(codexHome string, rolloutPath string) error {
	rolloutPath = filepath.Clean(strings.TrimSpace(rolloutPath))
	if !filepath.IsAbs(rolloutPath) {
		return fmt.Errorf("Codex rollout 路径不是绝对路径")
	}
	relative, err := filepath.Rel(codexHome, rolloutPath)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Codex rollout 不在 CODEX_HOME 内")
	}
	resolvedHome, err := filepath.EvalSymlinks(codexHome)
	if err != nil {
		return fmt.Errorf("解析 CODEX_HOME 路径: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(rolloutPath)
	if err != nil {
		return fmt.Errorf("解析 Codex rollout 路径: %w", err)
	}
	resolvedRelative, resolvedErr := filepath.Rel(resolvedHome, resolved)
	if resolvedErr != nil || filepath.Clean(resolvedRelative) != filepath.Clean(relative) {
		return fmt.Errorf("Codex rollout 路径包含符号链接")
	}
	if err := validateCodexProviderFile(rolloutPath); err != nil {
		return fmt.Errorf("验证 Codex rollout: %w", err)
	}
	return nil
}

func writeCodexProviderFileAtomically(filename string, data []byte) error {
	if err := validateCodexProviderFile(filename); err != nil {
		return err
	}
	dir := filepath.Dir(filename)
	temporary, err := os.CreateTemp(dir, ".weclaw-provider-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return err
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}
