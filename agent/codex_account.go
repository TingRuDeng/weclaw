package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/config"
)

type CodexAccountProfileID = codexauth.ProfileID

// CodexAccountProfile 是可安全输出到 CLI/API/卡片的脱敏视图。
type CodexAccountProfile struct {
	ID            CodexAccountProfileID   `json:"id"`
	Label         string                  `json:"label"`
	AuthMode      string                  `json:"auth_mode"`
	EmailMasked   string                  `json:"email_masked,omitempty"`
	SecretBackend codexauth.SecretBackend `json:"secret_backend"`
	CreatedAt     time.Time               `json:"created_at"`
	UpdatedAt     time.Time               `json:"updated_at"`
	LastUsedAt    *time.Time              `json:"last_used_at,omitempty"`
}

type CodexAccountStoreStatus struct {
	HostID      string                  `json:"host_id"`
	Revision    uint64                  `json:"revision"`
	Current     *CodexAccountProfile    `json:"current,omitempty"`
	Profiles    []CodexAccountProfile   `json:"profiles,omitempty"`
	LastSwitch  *codexauth.SwitchRecord `json:"last_switch,omitempty"`
	ManagedHost bool                    `json:"managed_host"`
}

type CodexAccountStatus struct {
	Store CodexAccountStoreStatus `json:"store"`
	Host  CodexHostStatus         `json:"host"`
	Quota *CodexQuota             `json:"quota,omitempty"`
}

type CodexAccountSwitchResult struct {
	Previous *CodexAccountProfile `json:"previous,omitempty"`
	Current  CodexAccountProfile  `json:"current"`
	Revision uint64               `json:"revision"`
	Changed  bool                 `json:"changed"`
	Quota    CodexQuota           `json:"quota"`
}

type CodexAccountSaveOptions struct {
	Label          string
	Replace        bool
	AllowFileStore bool
}

// CodexAccountAgent 是在线 shared app-server 的主机级账号控制接口。
type CodexAccountAgent interface {
	ListCodexAccounts(context.Context) (CodexAccountStatus, error)
	CurrentCodexAccount(context.Context, bool) (CodexAccountStatus, error)
	SaveCodexAccount(context.Context, CodexAccountSaveOptions) (CodexAccountProfile, error)
	UseCodexAccount(context.Context, string, uint64) (CodexAccountSwitchResult, error)
	RemoveCodexAccount(context.Context, string) error
	DoctorCodexAccounts(context.Context) codexauth.DoctorResult
}

// OpenOfflineCodexAccountStore 解析与运行时完全相同的 CODEX_HOME、socket 与
// host namespace，供服务停止时的本地 CLI 使用；它不会启动 app-server。
func OpenOfflineCodexAccountStore(cfg ACPAgentConfig) (*codexauth.Store, error) {
	return NewACPAgent(cfg).codexAccountStore()
}

func (a *ACPAgent) codexAccountStore() (*codexauth.Store, error) {
	if a.codexAccountStoreCall != nil {
		return a.codexAccountStoreCall()
	}
	if !a.usesCodexSharedHost() {
		return nil, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "当前 Agent 不是 Codex shared app-server", nil)
	}
	socketPath, err := a.resolveCodexHostSocket()
	if err != nil {
		return nil, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "无法解析 Codex Host socket", err)
	}
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return nil, err
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "无法解析 WeClaw 状态目录", err)
	}
	return codexauth.NewStore(codexauth.StoreOptions{
		DataDir: dataDir, CodexHome: codexHome, SocketPath: socketPath,
	})
}

func (a *ACPAgent) ListCodexAccounts(ctx context.Context) (CodexAccountStatus, error) {
	store, err := a.codexAccountStore()
	if err != nil {
		return CodexAccountStatus{}, err
	}
	status, err := store.Status()
	if err != nil {
		return CodexAccountStatus{}, err
	}
	host := a.InspectCodexHost(ctx)
	status.ManagedHost = host.Managed && host.Running
	return CodexAccountStatus{Store: publicCodexAccountStatus(status), Host: host}, nil
}

func (a *ACPAgent) CurrentCodexAccount(ctx context.Context, withQuota bool) (CodexAccountStatus, error) {
	status, err := a.ListCodexAccounts(ctx)
	if err != nil {
		return CodexAccountStatus{}, err
	}
	if !withQuota || status.Store.Current == nil || !a.isRuntimeStarted() {
		return status, nil
	}
	quota, err := a.ReadCodexQuota(ctx)
	if err != nil {
		return CodexAccountStatus{}, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "读取 Codex 额度失败", err)
	}
	status.Quota = &quota
	return status, nil
}

func (a *ACPAgent) SaveCodexAccount(ctx context.Context, options CodexAccountSaveOptions) (CodexAccountProfile, error) {
	gate := a.ensureCodexAppServerGate()
	if err := gate.beginExclusive(); err != nil {
		return CodexAccountProfile{}, mapCodexAccountBusy(err)
	}
	defer gate.finishExclusive(false, true)
	if err := a.ensureStarted(ctx); err != nil {
		return CodexAccountProfile{}, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "启动 Codex app-server 失败", err)
	}
	account, err := a.readCodexAccount(ctx, true)
	if err != nil {
		return CodexAccountProfile{}, err
	}
	store, err := a.codexAccountStore()
	if err != nil {
		return CodexAccountProfile{}, err
	}
	snapshot, err := codexauth.ReadAuthFile(store.AuthPath())
	if err != nil {
		return CodexAccountProfile{}, err
	}
	if !snapshot.MatchesEmail(account.Email) {
		return CodexAccountProfile{}, codexauth.NewError(codexauth.CodeTargetMismatch, "Codex 运行时账号与 auth.json 不一致", nil)
	}
	// 在线保存会把当前运行时身份写入主机级账户索引。必须先确认这里连接的
	// 是当前配置启动的唯一受管 Host，避免旧版或外部 app-server 被误登记为
	// WeClaw 的活动账户来源。
	if _, err := a.validateManagedCodexHost(store.SocketPath()); err != nil {
		return CodexAccountProfile{}, err
	}
	profile, err := store.Save(ctx, snapshot, codexauth.SaveOptions{
		Label: options.Label, Replace: options.Replace, AllowFileStore: options.AllowFileStore,
	})
	if err != nil {
		return CodexAccountProfile{}, err
	}
	if err := a.setManagedCodexHostAccountIdentity(store.SocketPath(), profile); err != nil {
		return CodexAccountProfile{}, err
	}
	return publicCodexAccountProfile(profile), nil
}

func (a *ACPAgent) RemoveCodexAccount(ctx context.Context, reference string) error {
	store, err := a.codexAccountStore()
	if err != nil {
		return err
	}
	return store.Remove(ctx, reference)
}

func (a *ACPAgent) DoctorCodexAccounts(ctx context.Context) codexauth.DoctorResult {
	store, err := a.codexAccountStore()
	if err != nil {
		return codexauth.DoctorResult{Message: err.Error()}
	}
	result := store.Doctor()
	host := a.InspectCodexHost(ctx)
	if !host.Managed || !host.Running {
		result.OK = false
		result.Message = "Codex 账号存储可读，但 shared app-server 不是可切换的受管 Host: " + host.Reason
	}
	return result
}

func (a *ACPAgent) UseCodexAccount(ctx context.Context, reference string, expectedRevision uint64) (result CodexAccountSwitchResult, err error) {
	gate := a.ensureCodexAppServerGate()
	if gateErr := gate.beginExclusive(); gateErr != nil {
		return result, mapCodexAccountBusy(gateErr)
	}
	committed := false
	available := true
	defer func() { gate.finishExclusive(committed, available) }()

	if err := a.ensureStarted(ctx); err != nil {
		return result, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "启动 Codex app-server 失败", err)
	}
	reportCodexAccountSwitchProgress(ctx, CodexAccountSwitchChecking)
	if a.codexOwners != nil {
		if count, uncertain := a.codexOwners.anyWriterLeaseStatus(); count > 0 {
			message := "Codex shared host 正有写入任务"
			if uncertain {
				message = "Codex shared host 存在终态未确认的写入任务"
			}
			return result, codexauth.NewError(codexauth.CodeBusy, message, nil)
		}
	}
	if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
		return result, err
	}

	currentAccount, err := a.readCodexAccount(ctx, true)
	if err != nil {
		return result, err
	}
	store, err := a.codexAccountStore()
	if err != nil {
		return result, err
	}
	liveSnapshot, err := codexauth.ReadAuthFile(store.AuthPath())
	if err != nil {
		return result, err
	}
	if !liveSnapshot.MatchesEmail(currentAccount.Email) {
		return result, codexauth.NewError(codexauth.CodeTargetMismatch, "Codex 运行时账号与 auth.json 不一致", nil)
	}

	err = store.WithTransaction(ctx, func(tx *codexauth.Transaction) error {
		index := tx.Index()
		if expectedRevision != 0 && index.Revision != expectedRevision {
			return codexauth.NewError(codexauth.CodeConflict, "Codex 账号列表已更新，请刷新后重试", nil)
		}
		target, ok := tx.Find(reference)
		if !ok {
			return codexauth.NewError(codexauth.CodeNotFound, "未找到目标 Codex 账号", nil)
		}
		targetSnapshot, readErr := tx.ReadSecret(target)
		if readErr != nil {
			return readErr
		}

		var previous *codexauth.Profile
		if index.ActiveProfileID != "" {
			active, found := tx.Find(string(index.ActiveProfileID))
			if !found {
				return codexauth.NewError(codexauth.CodeInvalid, "当前 Codex 账号索引损坏", nil)
			}
			if liveSnapshot.AccountFingerprint() != active.AccountFingerprint ||
				liveSnapshot.EmailFingerprint() != active.EmailFingerprint {
				return codexauth.NewError(codexauth.CodeTargetMismatch, "当前 Codex 登录账号与活动 profile 不一致；请先 save 当前账号", nil)
			}
			updated, replaceErr := tx.ReplaceProfileSnapshot(active, liveSnapshot)
			if replaceErr != nil {
				return replaceErr
			}
			previous = &updated
			if flushErr := tx.Flush(); flushErr != nil {
				return flushErr
			}
		}

		if previous != nil && previous.ID == target.ID {
			if refreshed, found := tx.Find(string(target.ID)); found {
				target = refreshed
			}
			if err := a.setManagedCodexHostAccountIdentity(store.SocketPath(), target); err != nil {
				return err
			}
			quota, quotaErr := a.ReadCodexQuota(ctx)
			if quotaErr != nil {
				return codexauth.NewError(codexauth.CodeRuntimeUnavailable, "读取 Codex 额度失败", quotaErr)
			}
			tx.SetLastSwitch(codexauth.SwitchRecord{ProfileID: target.ID, Status: "unchanged", Message: "已是当前账号", At: time.Now()})
			if err := tx.Flush(); err != nil {
				return err
			}
			latest := tx.Index()
			result = CodexAccountSwitchResult{
				Previous: publicCodexAccountProfilePtr(previous), Current: publicCodexAccountProfile(target),
				Revision: latest.Revision, Changed: false, Quota: quota,
			}
			return nil
		}

		lifecycleLock, lockErr := a.acquireCodexHostStartupLock(ctx, store.SocketPath())
		if lockErr != nil {
			return codexauth.NewError(codexauth.CodeBusy, "Codex Host 正在执行其他生命周期操作", lockErr)
		}
		defer releaseCodexHostStartupLock(lifecycleLock)
		// 刷新当前账号并回填 profile 可能涉及系统凭据库，真正停止 Host 前必须
		// 再确认一次全局 thread 状态，避免使用过期的空闲结论切断晚到任务。
		if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
			return err
		}
		reportCodexAccountSwitchProgress(ctx, CodexAccountSwitchSwitching)

		if err := a.stopManagedHost(ctx, store.SocketPath()); err != nil {
			return err
		}
		hostStopped := true
		rollback := func(switchErr error) error {
			reportCodexAccountSwitchProgress(ctx, CodexAccountSwitchRollback)
			usageRestoreErr := tx.RestoreProfileUsage(target)
			activeRestoreErr := tx.RestoreActive(index.ActiveProfileID)
			rollbackErr := a.rollbackCodexAccountSwitch(ctx, store, liveSnapshot, previous)
			if usageRestoreErr != nil || activeRestoreErr != nil || rollbackErr != nil {
				available = false
				tx.SetLastSwitch(codexauth.SwitchRecord{ProfileID: index.ActiveProfileID, Status: "rollback_failed", Message: "账号切换失败且旧运行时恢复失败", At: time.Now()})
				_ = tx.Flush()
				return codexauth.NewError(codexauth.CodeRollbackFailed, "Codex 账号切换失败，旧账号或账号索引未能完整恢复；当前已禁止继续写入", errors.Join(switchErr, usageRestoreErr, activeRestoreErr, rollbackErr))
			}
			hostStopped = false
			tx.SetLastSwitch(codexauth.SwitchRecord{ProfileID: index.ActiveProfileID, Status: "rolled_back", Message: "目标账号不可用，已恢复原账号", At: time.Now()})
			if flushErr := tx.Flush(); flushErr != nil {
				return codexauth.NewError(codexauth.CodeRuntimeUnavailable, "旧账号已恢复，但切换结果记录失败", flushErr)
			}
			return switchErr
		}

		if err := codexauth.WriteAuthFile(store.AuthPath(), targetSnapshot); err != nil {
			if hostStopped {
				return rollback(err)
			}
			return err
		}
		if err := a.startManagedHost(ctx, store.SocketPath()); err != nil {
			return rollback(codexauth.NewError(codexauth.CodeRuntimeUnavailable, "目标账号的 Codex Host 启动失败", err))
		}
		reportCodexAccountSwitchProgress(ctx, CodexAccountSwitchVerifying)
		verified, verifyErr := a.readCodexAccount(ctx, false)
		if verifyErr != nil {
			return rollback(verifyErr)
		}
		if !targetSnapshot.MatchesEmail(verified.Email) {
			return rollback(codexauth.NewError(codexauth.CodeTargetMismatch, "启动后的 Codex Host 不是目标账号", nil))
		}
		quota, quotaErr := a.ReadCodexQuota(ctx)
		if quotaErr != nil {
			return rollback(codexauth.NewError(codexauth.CodeRuntimeUnavailable, "目标账号额度验证失败", quotaErr))
		}
		if err := a.setManagedCodexHostAccountIdentity(store.SocketPath(), target); err != nil {
			return rollback(err)
		}
		if err := tx.SetActive(target.ID); err != nil {
			return rollback(err)
		}
		tx.SetLastSwitch(codexauth.SwitchRecord{ProfileID: target.ID, Status: "success", Message: "账号切换成功", At: time.Now()})
		if err := tx.Flush(); err != nil {
			return rollback(err)
		}
		a.markAllCodexThreadsResumeOnFirstUse()
		latest := tx.Index()
		result = CodexAccountSwitchResult{
			Previous: publicCodexAccountProfilePtr(previous), Current: publicCodexAccountProfile(target),
			Revision: latest.Revision, Changed: true, Quota: quota,
		}
		return nil
	})
	if err != nil {
		return CodexAccountSwitchResult{}, err
	}
	committed = result.Changed
	return result, nil
}

func (a *ACPAgent) stopManagedHost(ctx context.Context, socketPath string) error {
	if a.stopManagedHostCall != nil {
		return a.stopManagedHostCall(ctx, socketPath)
	}
	return a.stopManagedCodexHostLocked(ctx, socketPath)
}

func (a *ACPAgent) startManagedHost(ctx context.Context, socketPath string) error {
	if a.startManagedHostCall != nil {
		return a.startManagedHostCall(ctx, socketPath)
	}
	return a.startManagedCodexHostLocked(ctx, socketPath)
}

func (a *ACPAgent) rollbackCodexAccountSwitch(ctx context.Context, store *codexauth.Store, live *codexauth.Snapshot, previous *codexauth.Profile) error {
	if a.isRuntimeStarted() {
		if err := a.stopManagedHost(ctx, store.SocketPath()); err != nil {
			return fmt.Errorf("stop failed target host: %w", err)
		}
	}
	if err := codexauth.WriteAuthFile(store.AuthPath(), live); err != nil {
		return fmt.Errorf("restore previous auth: %w", err)
	}
	if err := a.startManagedHost(ctx, store.SocketPath()); err != nil {
		return fmt.Errorf("restart previous host: %w", err)
	}
	account, err := a.readCodexAccount(ctx, false)
	if err != nil {
		return fmt.Errorf("verify previous account: %w", err)
	}
	if !live.MatchesEmail(account.Email) {
		return fmt.Errorf("previous account identity mismatch")
	}
	if _, err := a.ReadCodexQuota(ctx); err != nil {
		return fmt.Errorf("verify previous quota: %w", err)
	}
	if previous != nil {
		if err := a.setManagedCodexHostAccountIdentity(store.SocketPath(), *previous); err != nil {
			return err
		}
	}
	a.markAllCodexThreadsResumeOnFirstUse()
	return nil
}

func (a *ACPAgent) markAllCodexThreadsResumeOnFirstUse() {
	a.mu.Lock()
	for conversationID := range a.threads {
		a.resumeOnFirstUse[conversationID] = true
	}
	a.mu.Unlock()
}

func (a *ACPAgent) setManagedCodexHostAccountIdentity(socketPath string, profile codexauth.Profile) error {
	if a.updateHostIdentityCall != nil {
		return a.updateHostIdentityCall(socketPath, profile)
	}
	return a.updateCodexHostAccountIdentity(socketPath, profile)
}

type codexAccountReadResponse struct {
	Account *struct {
		Type     string `json:"type"`
		Email    string `json:"email"`
		PlanType string `json:"planType"`
	} `json:"account"`
	RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
}

type codexRuntimeAccount struct {
	Email    string
	PlanType string
}

func (a *ACPAgent) readCodexAccount(ctx context.Context, refresh bool) (codexRuntimeAccount, error) {
	result, err := a.rpc(ctx, "account/read", map[string]bool{"refreshToken": refresh})
	if err != nil {
		return codexRuntimeAccount{}, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "读取 Codex 当前账号失败", err)
	}
	var response codexAccountReadResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return codexRuntimeAccount{}, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "解析 Codex 当前账号失败", err)
	}
	if response.Account == nil || response.RequiresOpenAIAuth {
		return codexRuntimeAccount{}, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "Codex 当前未登录", nil)
	}
	if response.Account.Type != "chatgpt" {
		return codexRuntimeAccount{}, codexauth.NewError(codexauth.CodeUnsupportedAuth, "仅支持 Codex ChatGPT OAuth 账号", nil)
	}
	email := strings.TrimSpace(response.Account.Email)
	if email == "" {
		return codexRuntimeAccount{}, codexauth.NewError(codexauth.CodeTargetMismatch, "Codex 当前账号缺少可验证邮箱", nil)
	}
	return codexRuntimeAccount{Email: email, PlanType: strings.TrimSpace(response.Account.PlanType)}, nil
}

type codexThreadListResponse struct {
	Data []struct {
		ID     string            `json:"id"`
		Status codexThreadStatus `json:"status"`
	} `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

func (a *ACPAgent) ensureAllCodexThreadsIdle(ctx context.Context) error {
	for _, archived := range []bool{false, true} {
		cursor := ""
		seen := make(map[string]struct{})
		completed := false
		for page := 0; page < 10000; page++ {
			params := map[string]interface{}{
				"archived": archived, "limit": 100,
			}
			if cursor != "" {
				params["cursor"] = cursor
			}
			result, err := a.rpc(ctx, "thread/list", params)
			if err != nil {
				return codexauth.NewError(codexauth.CodeBusy, "无法确认所有 Codex thread 均为空闲，拒绝切换账号", err)
			}
			var response codexThreadListResponse
			if err := json.Unmarshal(result, &response); err != nil {
				return codexauth.NewError(codexauth.CodeBusy, "Codex thread 状态无法解析，拒绝切换账号", err)
			}
			for _, thread := range response.Data {
				switch thread.Status.Type {
				case "idle", "notLoaded":
				case "active":
					return codexauth.NewError(codexauth.CodeBusy, "Codex 仍有运行中的 thread，不能切换账号", nil)
				default:
					return codexauth.NewError(codexauth.CodeBusy, "Codex thread 运行态未知，不能切换账号", nil)
				}
			}
			if response.NextCursor == nil || strings.TrimSpace(*response.NextCursor) == "" {
				completed = true
				break
			}
			next := strings.TrimSpace(*response.NextCursor)
			if _, duplicate := seen[next]; duplicate {
				return codexauth.NewError(codexauth.CodeBusy, "Codex thread 分页游标重复，无法确认全局空闲", nil)
			}
			seen[next] = struct{}{}
			cursor = next
		}
		if !completed {
			return codexauth.NewError(codexauth.CodeBusy, "Codex thread 列表过大，无法确认全局空闲", nil)
		}
	}
	return nil
}

func mapCodexAccountBusy(err error) error {
	if errors.Is(err, ErrCodexWriterBusy) {
		return codexauth.NewError(codexauth.CodeBusy, "Codex shared host 正有运行任务或维护操作", err)
	}
	if errors.Is(err, ErrCodexRuntimeUnavailable) {
		return codexauth.NewError(codexauth.CodeRuntimeUnavailable, "Codex shared host 当前不可写", err)
	}
	return err
}

func publicCodexAccountProfile(profile codexauth.Profile) CodexAccountProfile {
	return CodexAccountProfile{
		ID: profile.ID, Label: profile.Label, AuthMode: profile.AuthMode, EmailMasked: profile.EmailMasked,
		SecretBackend: profile.SecretBackend, CreatedAt: profile.CreatedAt, UpdatedAt: profile.UpdatedAt,
		LastUsedAt: profile.LastUsedAt,
	}
}

func publicCodexAccountProfilePtr(profile *codexauth.Profile) *CodexAccountProfile {
	if profile == nil {
		return nil
	}
	result := publicCodexAccountProfile(*profile)
	return &result
}

func publicCodexAccountStatus(status codexauth.Status) CodexAccountStoreStatus {
	result := CodexAccountStoreStatus{
		HostID: status.HostID, Revision: status.Revision, LastSwitch: status.LastSwitch,
		ManagedHost: status.ManagedHost, Profiles: make([]CodexAccountProfile, 0, len(status.Profiles)),
	}
	for _, profile := range status.Profiles {
		result.Profiles = append(result.Profiles, publicCodexAccountProfile(profile))
	}
	if status.Current != nil {
		current := publicCodexAccountProfile(*status.Current)
		result.Current = &current
	}
	return result
}
