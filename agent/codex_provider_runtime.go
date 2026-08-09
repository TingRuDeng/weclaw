package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

var errCodexProviderMigrationDeferred = fmt.Errorf("%w: Codex provider 迁移等待 Host 空闲", ErrCodexWriterBusy)

// PrepareCodexThread makes one persisted historical thread compatible with
// the effective provider of the current Codex Host before it can be resumed.
func (a *ACPAgent) PrepareCodexThread(ctx context.Context, req CodexRuntimeRequest) (CodexProviderPreparation, error) {
	if a.protocol != protocolCodexAppServer || strings.TrimSpace(req.WorkspaceRoot) == "" {
		return CodexProviderPreparation{}, nil
	}
	a.codexAdmissionMu.Lock()
	defer a.codexAdmissionMu.Unlock()
	return a.prepareCodexThreadProviderLocked(ctx, req)
}

func (a *ACPAgent) prepareCodexThreadProviderLocked(ctx context.Context, req CodexRuntimeRequest) (result CodexProviderPreparation, err error) {
	if strings.TrimSpace(req.WorkspaceRoot) == "" {
		return result, nil
	}
	workspaceRoot := filepath.Clean(strings.TrimSpace(req.WorkspaceRoot))
	if !filepath.IsAbs(workspaceRoot) {
		return result, fmt.Errorf("Codex workspace 必须是绝对路径")
	}
	threadID := strings.TrimSpace(req.Ref.ThreadID)
	if !codexThreadIDPattern.MatchString(threadID) {
		return result, fmt.Errorf("Codex thread ID 无效")
	}
	if err := a.ensureStarted(ctx); err != nil {
		return result, fmt.Errorf("启动 Codex Host 以读取当前 provider: %w", err)
	}

	provider, err := a.readEffectiveCodexProvider(ctx, workspaceRoot)
	if err != nil {
		return result, err
	}
	result.Provider = provider
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return result, fmt.Errorf("解析 CODEX_HOME: %w", err)
	}
	stateRow, err := readCodexProviderStateRow(ctx, filepath.Join(codexHome, "state_5.sqlite"), threadID)
	if err != nil {
		return result, err
	}
	result.PreviousProvider = stateRow.ModelProvider
	if stateRow.ModelProvider == provider {
		a.rememberCodexThreadProvider(threadID, provider)
		return result, nil
	}
	if a.codexProviderMigrationBusy(req) {
		result.Deferred = true
		result.TargetActive = true
		return result, nil
	}

	gate := a.ensureCodexAppServerGate()
	if err := gate.beginExclusive(); err != nil {
		return result, err
	}
	committed := false
	available := true
	defer func() { gate.finishExclusive(committed, available) }()
	if a.codexOwners != nil {
		if count, uncertain := a.codexOwners.anyWriterLeaseStatus(); count > 0 {
			message := ErrCodexWriterBusy
			if uncertain {
				message = fmt.Errorf("%w: 存在终态未确认的写入任务", ErrCodexWriterBusy)
			}
			return result, message
		}
	}

	mode := a.codexRuntimeModeSnapshot()
	switch mode {
	case CodexRuntimeDesktop:
		if deferred, checkErr := a.ensureCodexDesktopTargetIdle(ctx, req); checkErr != nil {
			return result, checkErr
		} else if deferred {
			result.Deferred = true
			result.TargetActive = true
			return result, nil
		}
		if a.codexOwners != nil {
			if active, unknown := a.codexOwners.anyActiveThreadStatus(); active > 0 || unknown {
				result.Deferred = true
				return result, nil
			}
		}
		available = false
		if err := a.stopCodexDesktopProviderHost(ctx); err != nil {
			return result, fmt.Errorf("停止 Codex App 以迁移 provider: %w", err)
		}
	case CodexRuntimeWeClaw:
		if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
			result.Deferred = true
			return result, nil
		}
		available = false
	default:
		return result, fmt.Errorf("%w: 无法确认当前 Codex Host", ErrCodexRuntimeUnavailable)
	}

	var socketPath string
	if mode == CodexRuntimeWeClaw {
		socketPath, err = a.resolveCodexHostSocket()
		if err != nil {
			available = true
			return result, err
		}
		lock, lockErr := a.acquireCodexHostStartupLock(ctx, socketPath)
		if lockErr != nil {
			available = true
			return result, lockErr
		}
		defer releaseCodexHostStartupLock(lock)
		if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
			available = true
			result.Deferred = true
			return result, nil
		}
		if err := a.stopManagedHost(ctx, socketPath); err != nil {
			return result, fmt.Errorf("停止 shared Codex Host 以迁移 provider: %w", err)
		}
	}
	migrate := a.codexProviderMigrationCall
	if migrate == nil {
		migrate = func(ctx context.Context, migration codexProviderMigrationRequest) (codexProviderMigrationResult, error) {
			return migrateCodexThreadProvider(ctx, migration)
		}
	}
	migration, migrationErr := migrate(ctx, codexProviderMigrationRequest{
		CodexHome: codexHome, ThreadID: threadID, TargetProvider: provider,
	})
	if migrationErr != nil {
		restartErr := a.restartCodexProviderHost(ctx, mode, socketPath, req)
		available = restartErr == nil
		return result, errors.Join(migrationErr, restartErr)
	}
	result.Changed = migration.Changed
	result.BackupDir = migration.BackupDir
	if err := a.restartCodexProviderHost(ctx, mode, socketPath, req); err != nil {
		return result, err
	}
	available = true
	verified, verifyErr := a.readEffectiveCodexProvider(ctx, workspaceRoot)
	if verifyErr != nil || verified != provider {
		available = false
		return result, fmt.Errorf("Codex Host provider 核验失败: provider=%q: %w", verified, verifyErr)
	}
	if mode == CodexRuntimeWeClaw {
		a.rememberCodexThreadProvider(threadID, provider)
		if err := a.resumeThreadWithProvider(ctx, req.Ref.ConversationID, threadID, provider); err != nil {
			available = false
			return result, fmt.Errorf("迁移后的 Codex thread resume 未确认: %w", err)
		}
	} else {
		a.rememberCodexThreadProvider(threadID, provider)
	}
	if migration.BackupDir != "" {
		if err := markCodexProviderMigrationVerified(migration.BackupDir); err != nil {
			available = false
			return result, err
		}
	}
	committed = true
	return result, nil
}

func (a *ACPAgent) codexProviderMigrationBusy(req CodexRuntimeRequest) bool {
	if req.Checkpoint.Active {
		return true
	}
	if a.codexOwners == nil {
		return false
	}
	if exists, _ := a.codexOwners.writerLeaseStatus(req.Ref.ThreadID); exists {
		return true
	}
	binding, ok := a.codexOwners.threadBinding(req.Ref.ThreadID)
	return ok && binding.State.Active
}

func (a *ACPAgent) ensureCodexDesktopTargetIdle(ctx context.Context, req CodexRuntimeRequest) (bool, error) {
	if a.desktopProbe == nil {
		return false, ErrCodexDesktopOwnershipUnknown
	}
	if err := a.desktopProbe.LoadHistory(ctx, req.Ref); err != nil {
		return false, err
	}
	if a.codexOwners == nil {
		return false, nil
	}
	binding, ok := a.codexOwners.threadBinding(req.Ref.ThreadID)
	return ok && binding.State.Active, nil
}

func (a *ACPAgent) restartCodexProviderHost(ctx context.Context, mode CodexRuntimeHolder, socketPath string, req CodexRuntimeRequest) error {
	switch mode {
	case CodexRuntimeDesktop:
		if err := a.startCodexDesktopProviderHost(ctx); err != nil {
			return fmt.Errorf("重启 Codex App: %w", err)
		}
		if a.desktopProbe == nil {
			return ErrCodexDesktopUnavailable
		}
		if err := a.desktopProbe.LoadHistory(ctx, req.Ref); err != nil {
			return fmt.Errorf("重载迁移后的 Codex Desktop thread: %w", err)
		}
		return nil
	case CodexRuntimeWeClaw:
		if err := a.startManagedHost(ctx, socketPath); err != nil {
			return fmt.Errorf("重启 shared Codex Host: %w", err)
		}
		a.setCodexRuntimeMode(CodexRuntimeWeClaw)
		return nil
	default:
		return ErrCodexRuntimeUnavailable
	}
}

func (a *ACPAgent) stopCodexDesktopProviderHost(ctx context.Context) error {
	if a.desktopRuntime != nil {
		if err := a.desktopRuntime.disconnect(); err != nil {
			return err
		}
	}
	stop := a.stopDesktopHostCall
	if stop == nil {
		stop = stopSystemCodexDesktopApp
	}
	if err := stop(ctx); err != nil {
		return err
	}
	a.setCodexRuntimeMode(CodexRuntimeUnknown)
	return nil
}

func (a *ACPAgent) startCodexDesktopProviderHost(ctx context.Context) error {
	start := a.startDesktopHostCall
	if start == nil {
		start = startSystemCodexDesktopApp
	}
	if err := start(ctx); err != nil {
		return err
	}
	if a.desktopRuntime == nil {
		return ErrCodexDesktopUnavailable
	}
	if err := a.desktopRuntime.connect(ctx); err != nil {
		return err
	}
	a.setCodexRuntimeMode(CodexRuntimeDesktop)
	return nil
}

func (a *ACPAgent) readEffectiveCodexProvider(ctx context.Context, workspaceRoot string) (string, error) {
	if a.codexProviderReadCall != nil {
		return a.codexProviderReadCall(ctx, workspaceRoot)
	}
	if a.codexRuntimeModeSnapshot() == CodexRuntimeDesktop {
		return a.readEffectiveCodexProviderFromFiles(workspaceRoot)
	}
	result, err := a.rpc(ctx, "config/read", map[string]string{"cwd": workspaceRoot})
	if err != nil {
		return "", fmt.Errorf("读取 Codex 当前 provider: %w", err)
	}
	var response struct {
		Config struct {
			ModelProvider string `json:"model_provider"`
		} `json:"config"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return "", fmt.Errorf("解析 Codex 当前 provider: %w", err)
	}
	provider := strings.TrimSpace(response.Config.ModelProvider)
	if provider == "" {
		provider = "openai"
	}
	if !codexProviderTokenPattern.MatchString(provider) {
		return "", fmt.Errorf("Codex 当前 provider %q 无效", provider)
	}
	return provider, nil
}

func (a *ACPAgent) readEffectiveCodexProviderFromFiles(workspaceRoot string) (string, error) {
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return "", err
	}
	provider := "openai"
	files := []string{filepath.Join(codexHome, "config.toml")}
	if profile := codexProfileFromArgs(a.args); profile != "" {
		files = append(files, filepath.Join(codexHome, profile+".config.toml"))
	}
	files = append(files, codexWorkspaceConfigFiles(workspaceRoot)...)
	for _, filename := range files {
		value, found, readErr := readRootCodexProvider(filename)
		if readErr != nil {
			return "", readErr
		}
		if found {
			provider = value
		}
	}
	if override := codexProviderOverrideFromArgs(a.args); override != "" {
		provider = override
	}
	if !codexProviderTokenPattern.MatchString(provider) {
		return "", fmt.Errorf("Codex 当前 provider %q 无效", provider)
	}
	return provider, nil
}

func readRootCodexProvider(filename string) (string, bool, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("Codex 配置必须是实体普通文件: %s", filename)
	}
	if err := validateCodexProviderOwner(info); err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", false, err
	}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			break
		}
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "model_provider" {
			continue
		}
		value := strings.TrimSpace(stripCodexTOMLComment(rawValue))
		if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") && len(value) >= 2 {
			value = strings.TrimSuffix(strings.TrimPrefix(value, "'"), "'")
		} else if unquoted, unquoteErr := strconv.Unquote(value); unquoteErr == nil {
			value = unquoted
		}
		value = strings.TrimSpace(value)
		if !codexProviderTokenPattern.MatchString(value) {
			return "", false, fmt.Errorf("Codex 配置中的 model_provider %q 无效", value)
		}
		return value, true, nil
	}
	return "", false, nil
}

func stripCodexTOMLComment(value string) string {
	quote := rune(0)
	escaped := false
	for index, character := range value {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quote == '"' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '#' {
			return value[:index]
		}
	}
	return value
}

func codexWorkspaceConfigFiles(workspaceRoot string) []string {
	workspaceRoot = filepath.Clean(workspaceRoot)
	var reversed []string
	for current := workspaceRoot; ; current = filepath.Dir(current) {
		reversed = append(reversed, filepath.Join(current, ".codex", "config.toml"))
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	files := make([]string, 0, len(reversed))
	for index := len(reversed) - 1; index >= 0; index-- {
		files = append(files, reversed[index])
	}
	return files
}

func codexProfileFromArgs(args []string) string {
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "-p", "--profile":
			if index+1 < len(args) && codexProviderTokenPattern.MatchString(args[index+1]) {
				return args[index+1]
			}
		default:
			if value, ok := strings.CutPrefix(args[index], "--profile="); ok && codexProviderTokenPattern.MatchString(value) {
				return value
			}
		}
	}
	return ""
}

func codexProviderOverrideFromArgs(args []string) string {
	provider := ""
	for index := 0; index < len(args); index++ {
		value := ""
		if args[index] == "-c" || args[index] == "--config" {
			if index+1 < len(args) {
				value = args[index+1]
			}
		} else if candidate, ok := strings.CutPrefix(args[index], "--config="); ok {
			value = candidate
		}
		key, rawProvider, ok := strings.Cut(value, "=")
		if !ok || strings.TrimSpace(key) != "model_provider" {
			continue
		}
		candidate := strings.Trim(strings.TrimSpace(rawProvider), "\"'")
		if codexProviderTokenPattern.MatchString(candidate) {
			provider = candidate
		}
	}
	return provider
}

func (a *ACPAgent) rememberCodexThreadProvider(threadID string, provider string) {
	a.mu.Lock()
	if a.codexThreadProviders == nil {
		a.codexThreadProviders = make(map[string]string)
	}
	a.codexThreadProviders[strings.TrimSpace(threadID)] = strings.TrimSpace(provider)
	a.mu.Unlock()
}

func (a *ACPAgent) codexThreadProvider(threadID string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.TrimSpace(a.codexThreadProviders[strings.TrimSpace(threadID)])
}
