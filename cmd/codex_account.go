package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const codexAccountAPITimeout = 2 * time.Minute

var codexAccountHTTPClient = &http.Client{Timeout: codexAccountAPITimeout}

var codexCmd = &cobra.Command{
	Use:   "codex",
	Short: "管理 Codex shared app-server",
}

var codexAccountCmd = &cobra.Command{
	Use:   "account",
	Short: "管理主机级 Codex OAuth 账号",
}

var codexAccountListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出已保存的 Codex 账号",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := loadCodexAccountStatus(cmd.Context(), false)
		if err != nil {
			return err
		}
		printCodexAccountList(status)
		return nil
	},
}

var codexAccountCurrentCmd = &cobra.Command{
	Use:   "current",
	Short: "查看当前 Codex 账号",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		status, err := loadCodexAccountStatus(cmd.Context(), true)
		if err != nil {
			return err
		}
		printCurrentCodexAccount(status)
		return nil
	},
}

var codexAccountSaveOptions struct {
	replace        bool
	allowFileStore bool
}

var codexAccountSaveCmd = &cobra.Command{
	Use:   "save <label>",
	Short: "保存当前 Codex ChatGPT OAuth 账号",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, online, err := loadCodexAccountRuntime()
		if err != nil {
			return err
		}
		if online {
			var response struct {
				Profile agent.CodexAccountProfile `json:"profile"`
			}
			err = callCodexAccountAPI(cmd.Context(), cfg, http.MethodPost, "/api/codex/accounts/save", map[string]any{
				"label": args[0], "replace": codexAccountSaveOptions.replace,
				"allow_file_store": codexAccountSaveOptions.allowFileStore,
			}, &response)
			if err != nil {
				return err
			}
			fmt.Printf("已保存 Codex 账号：%s（%s，%s）\n", response.Profile.Label, response.Profile.EmailMasked, response.Profile.SecretBackend)
			return nil
		}
		store, err := openOfflineCodexAccountStore(cfg)
		if err != nil {
			return err
		}
		profile, err := store.SaveAuthFile(cmd.Context(), codexauth.SaveOptions{
			Label: args[0], Replace: codexAccountSaveOptions.replace, AllowFileStore: codexAccountSaveOptions.allowFileStore,
		})
		if err != nil {
			return err
		}
		fmt.Printf("已离线保存 Codex 账号：%s（%s，%s）\n", profile.Label, profile.EmailMasked, profile.SecretBackend)
		return nil
	},
}

var codexAccountUseOptions struct{ yes bool }

var codexAccountUseCmd = &cobra.Command{
	Use:   "use <id-or-label>",
	Short: "切换主机级 Codex 账号",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, online, err := loadCodexAccountRuntime()
		if err != nil {
			return err
		}
		status, err := loadCodexAccountStatusWithRuntime(cmd.Context(), cfg, online, false)
		if err != nil {
			return err
		}
		target, ok := findPublicCodexAccount(status.Store.Profiles, args[0])
		if !ok {
			return codexauth.NewError(codexauth.CodeNotFound, "未找到目标 Codex 账号", nil)
		}
		currentLabel := "未记录"
		if status.Store.Current != nil {
			currentLabel = status.Store.Current.Label
		}
		if !codexAccountUseOptions.yes {
			confirmed, err := confirmCodexAccountUse(currentLabel, target.Label)
			if err != nil {
				return err
			}
			if !confirmed {
				fmt.Println("已取消 Codex 账号切换。")
				return nil
			}
		}
		if online {
			var response struct {
				Result agent.CodexAccountSwitchResult `json:"result"`
			}
			if err := callCodexAccountAPI(cmd.Context(), cfg, http.MethodPost, "/api/codex/accounts/use", map[string]any{
				"profile": string(target.ID), "expected_revision": status.Store.Revision,
			}, &response); err != nil {
				return err
			}
			if response.Result.Changed {
				fmt.Printf("Codex 账号已切换：%s → %s。现有工作空间和会话绑定保持不变。\n", currentLabel, response.Result.Current.Label)
			} else {
				fmt.Printf("Codex 当前已是账号：%s。\n", response.Result.Current.Label)
			}
			return nil
		}
		store, err := openOfflineCodexAccountStore(cfg)
		if err != nil {
			return err
		}
		profile, changed, err := useOfflineCodexAccount(cmd.Context(), store, string(target.ID), status.Store.Revision)
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("已离线切换 Codex 账号为 %s；下次启动 WeClaw 时生效。\n", profile.Label)
		} else {
			fmt.Printf("Codex 当前认证已是账号：%s。\n", profile.Label)
		}
		return nil
	},
}

var codexAccountRemoveCmd = &cobra.Command{
	Use:   "remove <id-or-label>",
	Short: "删除非当前 Codex 账号",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, online, err := loadCodexAccountRuntime()
		if err != nil {
			return err
		}
		if online {
			if err := callCodexAccountAPI(cmd.Context(), cfg, http.MethodPost, "/api/codex/accounts/remove", map[string]string{"profile": args[0]}, nil); err != nil {
				return err
			}
		} else {
			store, err := openOfflineCodexAccountStore(cfg)
			if err != nil {
				return err
			}
			if err := store.Remove(cmd.Context(), args[0]); err != nil {
				return err
			}
		}
		fmt.Println("已删除 Codex 账号。")
		return nil
	},
}

var codexAccountDoctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "检查 Codex 账号存储与受管 Host",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, online, err := loadCodexAccountRuntime()
		if err != nil {
			return err
		}
		if online {
			var response struct {
				OK      bool   `json:"ok"`
				Message string `json:"message"`
				HostID  string `json:"host_id"`
			}
			if err := callCodexAccountAPI(cmd.Context(), cfg, http.MethodGet, "/api/codex/accounts/doctor", nil, &response); err != nil {
				return err
			}
			fmt.Printf("%s %s（host=%s）\n", doctorSymbol(response.OK), response.Message, response.HostID)
			if !response.OK {
				return fmt.Errorf("Codex 账号检查未通过")
			}
			return nil
		}
		store, err := openOfflineCodexAccountStore(cfg)
		if err != nil {
			return err
		}
		result := store.Doctor()
		fmt.Printf("%s %s\n", doctorSymbol(result.OK), result.Message)
		fmt.Printf("账号目录：%s\n认证文件：%s\n", result.Store, result.Auth)
		if !result.OK {
			return fmt.Errorf("Codex 账号检查未通过")
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(codexCmd)
	codexCmd.AddCommand(codexAccountCmd)
	codexAccountCmd.AddCommand(
		codexAccountListCmd, codexAccountCurrentCmd, codexAccountSaveCmd,
		codexAccountUseCmd, codexAccountRemoveCmd, codexAccountDoctorCmd,
	)
	codexAccountSaveCmd.Flags().BoolVar(&codexAccountSaveOptions.replace, "replace", false, "覆盖同名账号")
	codexAccountSaveCmd.Flags().BoolVar(&codexAccountSaveOptions.allowFileStore, "allow-file-store", false, "系统凭据库不可用时允许使用 0600 文件")
	codexAccountUseCmd.Flags().BoolVar(&codexAccountUseOptions.yes, "yes", false, "跳过交互确认")
}

func loadCodexAccountRuntime() (*config.Config, bool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, false, fmt.Errorf("加载配置失败: %w", err)
	}
	state, err := readRuntimeState()
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, false, nil
		}
		return nil, false, fmt.Errorf("无法确认 WeClaw 服务状态，已拒绝离线修改: %w", err)
	}
	if !processExists(state.PID) {
		return cfg, false, nil
	}
	return cfg, true, nil
}

func loadCodexAccountStatus(ctx context.Context, withQuota bool) (agent.CodexAccountStatus, error) {
	cfg, online, err := loadCodexAccountRuntime()
	if err != nil {
		return agent.CodexAccountStatus{}, err
	}
	return loadCodexAccountStatusWithRuntime(ctx, cfg, online, withQuota)
}

func loadCodexAccountStatusWithRuntime(ctx context.Context, cfg *config.Config, online bool, withQuota bool) (agent.CodexAccountStatus, error) {
	if online {
		path := "/api/codex/accounts"
		if withQuota {
			path = "/api/codex/accounts/current?quota=1"
		}
		var status agent.CodexAccountStatus
		if err := callCodexAccountAPI(ctx, cfg, http.MethodGet, path, nil, &status); err != nil {
			return agent.CodexAccountStatus{}, fmt.Errorf("WeClaw 服务正在运行，但本地账号控制 API 不可用，已拒绝直接修改认证: %w", err)
		}
		return status, nil
	}
	store, err := openOfflineCodexAccountStore(cfg)
	if err != nil {
		return agent.CodexAccountStatus{}, err
	}
	status, err := store.Status()
	if err != nil {
		return agent.CodexAccountStatus{}, err
	}
	return offlinePublicCodexAccountStatus(status), nil
}

func openOfflineCodexAccountStore(cfg *config.Config) (*codexauth.Store, error) {
	name, agentConfig, err := configuredCodexAppServer(cfg)
	if err != nil {
		return nil, err
	}
	return agent.OpenOfflineCodexAccountStore(acpAgentConfigFromConfig(name, agentConfig))
}

func configuredCodexAppServer(cfg *config.Config) (string, config.AgentConfig, error) {
	if candidate, ok := cfg.Agents["codex"]; ok && isCodexAppServerAgent(candidate) {
		return "codex", candidate, nil
	}
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	var foundName string
	var found config.AgentConfig
	for _, name := range names {
		candidate := cfg.Agents[name]
		if !isCodexAppServerAgent(candidate) {
			continue
		}
		if foundName != "" {
			return "", config.AgentConfig{}, fmt.Errorf("配置了多个 Codex app-server Agent，请保留唯一 shared Host")
		}
		foundName, found = name, candidate
	}
	if foundName == "" {
		return "", config.AgentConfig{}, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "未配置 Codex app-server Agent", nil)
	}
	return foundName, found, nil
}

func callCodexAccountAPI(ctx context.Context, cfg *config.Config, method, path string, payload any, target any) error {
	endpoint, err := runtimeAPIURL(cfg.APIAddr, path)
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token := strings.TrimSpace(cfg.APIToken); token != "" {
		request.Header.Set("X-WeClaw-Token", token)
	}
	response, err := codexAccountHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := decoder.Decode(&apiError); err != nil {
			return fmt.Errorf("账号控制 API 返回 HTTP %d", response.StatusCode)
		}
		return codexauth.NewError(apiError.Code, apiError.Message, nil)
	}
	if target == nil {
		return nil
	}
	return decoder.Decode(target)
}

func useOfflineCodexAccount(ctx context.Context, store *codexauth.Store, reference string, expectedRevision uint64) (codexauth.Profile, bool, error) {
	var selected codexauth.Profile
	changed := false
	err := store.WithTransaction(ctx, func(tx *codexauth.Transaction) error {
		index := tx.Index()
		if expectedRevision != 0 && index.Revision != expectedRevision {
			return codexauth.NewError(codexauth.CodeConflict, "Codex 账号列表已更新，请重试", nil)
		}
		profile, ok := tx.Find(reference)
		if !ok {
			return codexauth.NewError(codexauth.CodeNotFound, "未找到目标 Codex 账号", nil)
		}
		snapshot, err := tx.ReadSecret(profile)
		if err != nil {
			return err
		}
		previousData, err := codexauth.ReadAuthFileBytes(store.AuthPath())
		if err != nil {
			return codexauth.NewError(codexauth.CodeRuntimeUnavailable, "无法建立当前认证回滚点，已拒绝离线切换", err)
		}
		previousSnapshot, err := codexauth.ParseSnapshot(previousData)
		if err != nil {
			return err
		}
		changed = previousSnapshot.AccountFingerprint() != profile.AccountFingerprint
		if err := codexauth.WriteAuthFile(store.AuthPath(), snapshot); err != nil {
			return err
		}
		if err := tx.SetActive(profile.ID); err != nil {
			_ = codexauth.RestoreAuthFile(store.AuthPath(), previousData)
			return err
		}
		tx.SetLastSwitch(codexauth.SwitchRecord{ProfileID: profile.ID, Status: "offline", Message: "离线账号切换已写入，下次启动生效", At: time.Now()})
		if err := tx.Flush(); err != nil {
			if rollbackErr := codexauth.RestoreAuthFile(store.AuthPath(), previousData); rollbackErr != nil {
				return codexauth.NewError(codexauth.CodeRollbackFailed, "离线切换索引保存失败，且认证回滚失败", errors.Join(err, rollbackErr))
			}
			return err
		}
		selected = profile
		return nil
	})
	return selected, changed, err
}

func offlinePublicCodexAccountStatus(status codexauth.Status) agent.CodexAccountStatus {
	result := agent.CodexAccountStatus{Store: agent.CodexAccountStoreStatus{
		HostID: status.HostID, Revision: status.Revision, LastSwitch: status.LastSwitch,
		PendingSecretDeletes: status.PendingSecretDeletes,
		Profiles:             make([]agent.CodexAccountProfile, 0, len(status.Profiles)),
	}}
	for _, profile := range status.Profiles {
		public := offlinePublicCodexAccountProfile(profile)
		result.Store.Profiles = append(result.Store.Profiles, public)
		if status.Current != nil && status.Current.ID == profile.ID {
			current := public
			result.Store.Current = &current
		}
	}
	return result
}

func offlinePublicCodexAccountProfile(profile codexauth.Profile) agent.CodexAccountProfile {
	return agent.CodexAccountProfile{
		ID: profile.ID, Label: profile.Label, AuthMode: profile.AuthMode, EmailMasked: profile.EmailMasked,
		SecretBackend: profile.SecretBackend, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
		LastUsedAt: profile.LastUsedAt,
	}
}

func findPublicCodexAccount(profiles []agent.CodexAccountProfile, reference string) (agent.CodexAccountProfile, bool) {
	for _, profile := range profiles {
		if string(profile.ID) == strings.TrimSpace(reference) || strings.EqualFold(profile.Label, strings.TrimSpace(reference)) {
			return profile, true
		}
	}
	return agent.CodexAccountProfile{}, false
}

func confirmCodexAccountUse(current, target string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("非交互环境切换账号必须加 --yes")
	}
	fmt.Printf("Codex 账号将全局切换：%s → %s。现有会话绑定保留。确认？[y/N] ", current, target)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "确认":
		return true, nil
	default:
		return false, nil
	}
}

func printCodexAccountList(status agent.CodexAccountStatus) {
	if len(status.Store.Profiles) == 0 {
		fmt.Println("尚未保存 Codex 账号。请先运行 weclaw codex account save <label>。")
		return
	}
	fmt.Printf("Codex 账号（shared Host，全局生效，revision=%d）：\n", status.Store.Revision)
	printCodexAccountCleanupWarning(status)
	current := effectiveCodexAccountProfile(status)
	for _, profile := range status.Store.Profiles {
		marker := " "
		if current != nil && current.ID == profile.ID {
			marker = "*"
		}
		fmt.Printf("%s %s  %s  %s  id=%s\n", marker, profile.Label, profile.EmailMasked, profile.SecretBackend, profile.ID)
	}
	printCodexAccountSyncNotice(status)
}

func printCurrentCodexAccount(status agent.CodexAccountStatus) {
	current := effectiveCodexAccountProfile(status)
	if current == nil {
		fmt.Println("当前 Codex 账号尚未纳入 WeClaw 管理。")
		printCodexAccountSyncNotice(status)
		return
	}
	fmt.Printf("当前 Codex 账号：%s（%s，%s）\n", current.Label, current.EmailMasked, current.SecretBackend)
	printCodexAccountSyncNotice(status)
	printCodexAccountCleanupWarning(status)
	if status.Quota != nil {
		fmt.Printf("额度桶：%d\n", len(status.Quota.Limits))
	}
	if status.Host.Managed {
		fmt.Printf("shared Host：受管，pid=%d，generation=%d\n", status.Host.PID, status.Host.Generation)
	} else if status.Host.Reason != "" {
		fmt.Printf("shared Host：不可切换（%s）\n", status.Host.Reason)
	}
}

func effectiveCodexAccountProfile(status agent.CodexAccountStatus) *agent.CodexAccountProfile {
	if status.Sync.AuthProfile != nil &&
		(status.Sync.State == agent.CodexAccountSyncPending || status.Sync.State == agent.CodexAccountSyncSynced) {
		return status.Sync.AuthProfile
	}
	return status.Store.Current
}

func printCodexAccountSyncNotice(status agent.CodexAccountStatus) {
	switch status.Sync.State {
	case agent.CodexAccountSyncPending:
		fmt.Println("账号同步：检测到本地 Codex 已切号；下一次任务将在 shared Host 空闲时自动同步。")
	case agent.CodexAccountSyncUnsaved:
		fmt.Println("账号同步：本地 Codex 当前账号尚未保存到 WeClaw。")
	case agent.CodexAccountSyncRuntimeMismatch:
		fmt.Println("账号同步：shared Host 与本地 Codex 认证不一致，当前已禁止写入。")
	case agent.CodexAccountSyncRuntimeUnavailable:
		fmt.Println("账号同步：无法确认 shared Host 当前账号。")
	}
}

func printCodexAccountCleanupWarning(status agent.CodexAccountStatus) {
	if status.Store.PendingSecretDeletes > 0 {
		fmt.Printf("警告：%d 个旧 OAuth 凭据等待清理，请运行 weclaw codex account doctor。\n", status.Store.PendingSecretDeletes)
	}
}

func doctorSymbol(ok bool) string {
	if ok {
		return "[ok]"
	}
	return "[fail]"
}
