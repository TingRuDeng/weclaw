package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

// inspectCodexAccountStatus 对照 profile 索引、auth.json、Host 元数据和可选的
// 实时 account/read。它只读且不会因 status/list 命令隐式重启共享 Host。
func (a *ACPAgent) inspectCodexAccountStatus(ctx context.Context, inspectRuntime bool) (CodexAccountStatus, error) {
	store, err := a.codexAccountStore()
	if err != nil {
		return CodexAccountStatus{}, err
	}
	stored, err := store.Status()
	if err != nil {
		return CodexAccountStatus{}, err
	}
	host := a.InspectCodexHost(ctx)
	stored.ManagedHost = host.Managed && host.Running
	result := CodexAccountStatus{
		Store: publicCodexAccountStatus(stored),
		Host:  host,
		Sync: CodexAccountSyncStatus{
			State: CodexAccountSyncUnmanaged,
		},
	}
	if len(stored.Profiles) == 0 {
		return result, nil
	}

	snapshot, err := codexauth.ReadAuthFile(store.AuthPath())
	if err != nil {
		result.Sync.State = CodexAccountSyncRuntimeUnavailable
		result.Sync.Message = "无法读取本地 Codex 认证"
		return result, nil
	}
	authProfile, ok := findCodexProfileBySnapshot(stored.Profiles, snapshot)
	if !ok {
		result.Sync.State = CodexAccountSyncUnsaved
		result.Sync.Message = "本地 Codex 当前账号尚未保存到 WeClaw"
		return result, nil
	}
	publicAuth := publicCodexAccountProfile(authProfile)
	result.Sync.AuthProfile = &publicAuth

	activeMatches := stored.Current != nil && stored.Current.ID == authProfile.ID
	hostMatches := codexHostMatchesProfile(host, authProfile)
	if activeMatches && hostMatches {
		result.Sync.State = CodexAccountSyncSynced
	} else {
		result.Sync.State = CodexAccountSyncPending
		result.Sync.Message = "检测到本地 Codex 已切换账号，下一次任务将在空闲时自动同步"
	}

	if !inspectRuntime || !a.isRuntimeStarted() {
		return result, nil
	}
	account, err := a.readCodexAccount(ctx, false)
	if err != nil {
		currentStillRecorded := stored.Current != nil &&
			!activeMatches &&
			codexHostMatchesProfile(host, *stored.Current)
		if currentStillRecorded {
			result.Sync.State = CodexAccountSyncPending
			result.Sync.Message = "检测到本地 Codex 已切换账号；shared Host 当前账号无法在线确认，下一次任务将在空闲时受控重启并同步"
			return result, nil
		}
		result.Sync.State = CodexAccountSyncRuntimeUnavailable
		result.Sync.Message = "无法确认 shared Host 当前账号"
		return result, nil
	}
	if liveProfile, found := findCodexProfileByEmail(stored.Profiles, account.Email); found {
		publicLive := publicCodexAccountProfile(liveProfile)
		result.Sync.LiveProfile = &publicLive
	}
	if !snapshot.MatchesEmail(account.Email) {
		currentStillLive := stored.Current != nil &&
			codexHostMatchesProfile(host, *stored.Current) &&
			stored.Current.EmailFingerprint == codexauth.EmailFingerprint(account.Email)
		if currentStillLive && !activeMatches {
			// Codex App 已把 auth.json 切到另一个已保存 profile，而运行中的
			// Host 仍保持切换前的受管身份。这是可自动收敛的预期漂移。
			result.Sync.State = CodexAccountSyncPending
			result.Sync.Message = "检测到本地 Codex 已切换账号，下一次任务将在空闲时自动同步"
			return result, nil
		}
		result.Sync.State = CodexAccountSyncRuntimeMismatch
		result.Sync.Message = "shared Host 与本地 Codex 认证不一致，写入前必须先同步"
		return result, nil
	}
	if activeMatches && hostMatches {
		result.Sync.State = CodexAccountSyncSynced
		result.Sync.Message = ""
	}
	return result, nil
}

func findCodexProfileBySnapshot(profiles []codexauth.Profile, snapshot *codexauth.Snapshot) (codexauth.Profile, bool) {
	if snapshot == nil {
		return codexauth.Profile{}, false
	}
	for _, profile := range profiles {
		if profile.AccountFingerprint == snapshot.AccountFingerprint() &&
			profile.EmailFingerprint == snapshot.EmailFingerprint() {
			return profile, true
		}
	}
	return codexauth.Profile{}, false
}

func findCodexProfileByEmail(profiles []codexauth.Profile, email string) (codexauth.Profile, bool) {
	fingerprint := codexauth.EmailFingerprint(email)
	if fingerprint == "" {
		return codexauth.Profile{}, false
	}
	var matched codexauth.Profile
	found := false
	for _, profile := range profiles {
		if profile.EmailFingerprint != fingerprint {
			continue
		}
		if found {
			// 同一邮箱可能对应多个 ChatGPT account_id；account/read 只给
			// 邮箱时不能任选一个 profile 作为权威运行身份或回滚点。
			return codexauth.Profile{}, false
		}
		matched = profile
		found = true
	}
	return matched, found
}

func findCodexProfileByHostIdentity(profiles []codexauth.Profile, host codexHostMetadata) (codexauth.Profile, bool) {
	for _, profile := range profiles {
		if string(profile.ID) == host.ActiveProfileID &&
			profile.AccountFingerprint == host.AccountFingerprint {
			return profile, true
		}
	}
	return codexauth.Profile{}, false
}

func codexHostMatchesProfile(host CodexHostStatus, profile codexauth.Profile) bool {
	return host.Managed && host.Running &&
		host.ActiveProfileID == string(profile.ID) &&
		host.AccountFingerprint == profile.AccountFingerprint
}

// ensureCodexAccountForTurn 让 Codex App 对 auth.json 的外部切号在下一次真实
// 写入前收敛到唯一 shared Host。未知账号和无法证明的状态一律禁止写入。
func (a *ACPAgent) ensureCodexAccountForTurn(ctx context.Context) error {
	enabled, err := a.codexAccountIndexEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	status, err := a.inspectCodexAccountStatus(ctx, false)
	if err != nil {
		return err
	}
	switch status.Sync.State {
	case CodexAccountSyncUnmanaged, CodexAccountSyncSynced:
		return nil
	case CodexAccountSyncPending, CodexAccountSyncRuntimeMismatch:
		_, err := a.reconcileExternallyProjectedCodexAccount(ctx, 0)
		if err != nil {
			return err
		}
		return a.validateCodexAccountForWrite(ctx)
	case CodexAccountSyncUnsaved:
		return codexauth.NewError(
			codexauth.CodeTargetMismatch,
			"本地 Codex 已切换到尚未保存的账号；请先执行 weclaw codex account save <标签>",
			nil,
		)
	default:
		return codexauth.NewError(
			codexauth.CodeRuntimeUnavailable,
			"无法确认 Codex 当前账号，已拒绝开始任务",
			nil,
		)
	}
}

// validateCodexAccountForWrite 在 turn permit 已获取后做最后一次轻量文件与元数据
// 校验，避免自动同步结束后 auth.json 又被外部切换而向错误账号写入。
func (a *ACPAgent) validateCodexAccountForWrite(ctx context.Context) error {
	enabled, err := a.codexAccountIndexEnabled()
	if err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	status, err := a.inspectCodexAccountStatus(ctx, false)
	if err != nil {
		return err
	}
	switch status.Sync.State {
	case CodexAccountSyncUnmanaged, CodexAccountSyncSynced:
		return nil
	case CodexAccountSyncUnsaved:
		return codexauth.NewError(codexauth.CodeTargetMismatch, "本地 Codex 当前账号尚未保存，已拒绝写入", nil)
	default:
		return codexauth.NewError(codexauth.CodeBusy, "Codex 账号状态刚刚发生变化，请重试当前任务", nil)
	}
}

func (a *ACPAgent) codexAccountIndexEnabled() (bool, error) {
	if a.codexAccountStoreCall == nil && !a.usesCodexSharedHost() {
		return false, nil
	}
	store, err := a.codexAccountStore()
	if err != nil {
		return false, err
	}
	if _, err := os.Lstat(store.IndexPath()); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "无法检查 Codex 账号索引", err)
	}
	// rpcCall 是包内协议测试的显式替身，不能让这些测试意外读取开发机的
	// 真实账号索引。生产 shared Host 没有该 hook；一旦已经启用账号索引，
	// Host 身份不可验证时必须失败关闭，不能绕过账号写入门禁。
	if a.codexAccountStoreCall == nil && a.rpcCall != nil {
		return false, nil
	}
	if _, err := a.validateManagedCodexHost(store.SocketPath()); err != nil {
		return false, err
	}
	return true, nil
}

func (a *ACPAgent) authProjectionMatchesReference(reference string) bool {
	store, err := a.codexAccountStore()
	if err != nil {
		return false
	}
	status, err := store.Status()
	if err != nil {
		return false
	}
	snapshot, err := codexauth.ReadAuthFile(store.AuthPath())
	if err != nil {
		return false
	}
	profile, ok := findCodexProfileBySnapshot(status.Profiles, snapshot)
	if !ok {
		return false
	}
	reference = strings.TrimSpace(reference)
	return string(profile.ID) == reference || strings.EqualFold(profile.Label, reference)
}

// reconcileExternallyProjectedCodexAccount 收敛 Codex App 已经写入 auth.json 的
// 已保存账号。若 shared Host 已经使用该账号，只提交索引和 Host 元数据；否则
// 复用完整空闲检查、生命周期锁、验证和回滚边界。
func (a *ACPAgent) reconcileExternallyProjectedCodexAccount(
	ctx context.Context,
	expectedRevision uint64,
) (result CodexAccountSwitchResult, err error) {
	gate := a.ensureCodexAppServerGate()
	if gateErr := gate.beginExclusive(); gateErr != nil {
		return result, mapCodexAccountBusy(gateErr)
	}
	committed := false
	runtimeRefreshed := false
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

	store, err := a.codexAccountStore()
	if err != nil {
		return result, err
	}
	err = store.WithTransaction(ctx, func(tx *codexauth.Transaction) error {
		index := tx.Index()
		if expectedRevision != 0 && index.Revision != expectedRevision {
			return codexauth.NewError(codexauth.CodeConflict, "Codex 账号列表已更新，请刷新后重试", nil)
		}
		authSnapshot, readErr := codexauth.ReadAuthFile(store.AuthPath())
		if readErr != nil {
			return readErr
		}
		target, ok := findCodexProfileBySnapshot(index.Profiles, authSnapshot)
		if !ok {
			return codexauth.NewError(
				codexauth.CodeTargetMismatch,
				"本地 Codex 当前账号尚未保存；已拒绝自动同步",
				nil,
			)
		}
		var previous *codexauth.Profile
		if index.ActiveProfileID != "" {
			profile, found := tx.Find(string(index.ActiveProfileID))
			if !found {
				return codexauth.NewError(codexauth.CodeInvalid, "当前 Codex 账号索引损坏", nil)
			}
			previous = &profile
		}

		metadata, metadataErr := a.validateManagedCodexHost(store.SocketPath())
		if metadataErr != nil {
			return metadataErr
		}
		liveAccount, liveAccountErr := a.readCodexAccount(ctx, false)
		liveProfile, liveProfileKnown := findCodexProfileByEmail(index.Profiles, liveAccount.Email)
		metadataProfile, metadataProfileKnown := findCodexProfileByHostIdentity(index.Profiles, metadata)
		targetAlreadyLive := liveAccountErr == nil && authSnapshot.MatchesEmail(liveAccount.Email) &&
			((liveProfileKnown && liveProfile.ID == target.ID) ||
				(metadataProfileKnown && metadataProfile.ID == target.ID))
		targetAlreadyRecorded := previous != nil && previous.ID == target.ID &&
			metadata.ActiveProfileID == string(target.ID) &&
			metadata.AccountFingerprint == target.AccountFingerprint
		if targetAlreadyLive && targetAlreadyRecorded {
			quota, quotaErr := a.ReadCodexQuota(ctx)
			if quotaErr != nil {
				return codexauth.NewError(codexauth.CodeRuntimeUnavailable, "读取 Codex 额度失败", quotaErr)
			}
			result = CodexAccountSwitchResult{
				Previous: publicCodexAccountProfilePtr(previous),
				Current:  publicCodexAccountProfile(target),
				Revision: index.Revision,
				Changed:  false,
				Quota:    quota,
			}
			return nil
		}
		if targetAlreadyLive {
			changed := !targetAlreadyRecorded
			quota, quotaErr := a.ReadCodexQuota(ctx)
			if quotaErr != nil {
				return codexauth.NewError(codexauth.CodeRuntimeUnavailable, "目标账号额度验证失败", quotaErr)
			}
			updated, updateErr := tx.ReplaceProfileSnapshot(target, authSnapshot)
			if updateErr != nil {
				return updateErr
			}
			if setErr := tx.SetActive(updated.ID); setErr != nil {
				return setErr
			}
			setMetadataErr := a.setManagedCodexHostAccountIdentity(store.SocketPath(), updated)
			if setMetadataErr != nil {
				restoreErr := a.restoreManagedCodexHostMetadata(store.SocketPath(), metadata)
				if restoreErr != nil {
					available = false
					tx.SetLastSwitch(codexauth.SwitchRecord{
						ProfileID: updated.ID, Status: "rollback_failed",
						Message: "外部账号同步失败且 Host 元数据未能恢复", At: time.Now(),
					})
					_ = tx.Flush()
					return codexauth.NewError(
						codexauth.CodeRollbackFailed,
						"Codex 外部账号同步失败，Host 元数据未能恢复；当前已禁止继续写入",
						errors.Join(setMetadataErr, restoreErr),
					)
				}
				return setMetadataErr
			}
			tx.SetLastSwitch(codexauth.SwitchRecord{
				ProfileID: updated.ID, Status: "external_sync_success",
				Message: "已同步本地 Codex 当前账号", At: time.Now(),
			})
			if flushErr := tx.Flush(); flushErr != nil {
				restoreErr := a.restoreManagedCodexHostMetadata(store.SocketPath(), metadata)
				if restoreErr != nil {
					available = false
					return codexauth.NewError(
						codexauth.CodeRollbackFailed,
						"Codex 账号索引提交失败，Host 元数据也未能恢复；当前已禁止继续写入",
						errors.Join(flushErr, restoreErr),
					)
				}
				return flushErr
			}
			latest := tx.Index()
			result = CodexAccountSwitchResult{
				Previous: publicCodexAccountProfilePtr(previous),
				Current:  publicCodexAccountProfile(updated),
				Revision: latest.Revision,
				Changed:  changed,
				Quota:    quota,
			}
			return nil
		}

		var rollbackProfile *codexauth.Profile
		var rollbackSnapshot *codexauth.Snapshot
		if profile, found := findCodexProfileByHostIdentity(index.Profiles, metadata); found {
			snapshot, secretErr := tx.ReadSecret(profile)
			if secretErr != nil {
				return secretErr
			}
			if liveAccountErr != nil || snapshot.MatchesEmail(liveAccount.Email) {
				rollbackProfile = &profile
				rollbackSnapshot = snapshot
			}
		}

		lifecycleLock, lockErr := a.acquireCodexHostStartupLock(ctx, store.SocketPath())
		if lockErr != nil {
			return codexauth.NewError(codexauth.CodeBusy, "Codex Host 正在执行其他生命周期操作", lockErr)
		}
		defer releaseCodexHostStartupLock(lifecycleLock)
		if err := a.ensureAllCodexThreadsIdle(ctx); err != nil {
			return err
		}
		latestAuth, readErr := codexauth.ReadAuthFile(store.AuthPath())
		if readErr != nil {
			return readErr
		}
		if latestAuth.AccountFingerprint() != authSnapshot.AccountFingerprint() ||
			latestAuth.EmailFingerprint() != authSnapshot.EmailFingerprint() {
			return codexauth.NewError(codexauth.CodeConflict, "本地 Codex 账号再次发生变化，请重试", nil)
		}

		reportCodexAccountSwitchProgress(ctx, CodexAccountSwitchSwitching)
		tx.SetLastSwitch(codexauth.SwitchRecord{
			ProfileID: target.ID, Status: "external_syncing",
			Message: "正在同步本地 Codex 当前账号", At: time.Now(),
		})
		if flushErr := tx.Flush(); flushErr != nil {
			return flushErr
		}
		available = false
		if stopErr := a.stopManagedHost(ctx, store.SocketPath()); stopErr != nil {
			return codexauth.NewError(
				codexauth.CodeRuntimeUnavailable,
				"无法确认 Codex Host 已安全停止；当前已禁止继续写入",
				stopErr,
			)
		}

		rollback := func(syncErr error) error {
			reportCodexAccountSwitchProgress(ctx, CodexAccountSwitchRollback)
			if rollbackProfile == nil || rollbackSnapshot == nil {
				tx.SetLastSwitch(codexauth.SwitchRecord{
					ProfileID: target.ID, Status: "rollback_failed",
					Message: "外部账号同步失败且没有可验证的旧账号回滚点", At: time.Now(),
				})
				recordErr := tx.Flush()
				return codexauth.NewError(
					codexauth.CodeRollbackFailed,
					"Codex 外部账号同步失败且无法恢复旧运行时；当前已禁止继续写入",
					errors.Join(syncErr, recordErr),
				)
			}
			rollbackErr := a.rollbackCodexAccountSwitch(ctx, store, rollbackSnapshot, rollbackProfile)
			if rollbackErr != nil {
				tx.SetLastSwitch(codexauth.SwitchRecord{
					ProfileID: rollbackProfile.ID, Status: "rollback_failed",
					Message: "外部账号同步失败且旧运行时恢复失败", At: time.Now(),
				})
				recordErr := tx.Flush()
				return codexauth.NewError(
					codexauth.CodeRollbackFailed,
					"Codex 外部账号同步失败，旧账号未能恢复；当前已禁止继续写入",
					errors.Join(syncErr, rollbackErr, recordErr),
				)
			}
			if activeErr := tx.SetActive(rollbackProfile.ID); activeErr != nil {
				return codexauth.NewError(
					codexauth.CodeRollbackFailed,
					"旧账号运行时已恢复，但活动 profile 未能恢复；当前已禁止继续写入",
					errors.Join(syncErr, activeErr),
				)
			}
			tx.SetLastSwitch(codexauth.SwitchRecord{
				ProfileID: rollbackProfile.ID, Status: "external_sync_rolled_back",
				Message: "本地账号无法使用，已恢复同步前账号", At: time.Now(),
			})
			if flushErr := tx.Flush(); flushErr != nil {
				return codexauth.NewError(codexauth.CodeRuntimeUnavailable, "旧账号已恢复，但同步结果记录失败", flushErr)
			}
			available = true
			return syncErr
		}

		if startErr := a.startManagedHost(ctx, store.SocketPath()); startErr != nil {
			return rollback(codexauth.NewError(codexauth.CodeRuntimeUnavailable, "目标账号的 Codex Host 启动失败", startErr))
		}
		reportCodexAccountSwitchProgress(ctx, CodexAccountSwitchVerifying)
		verified, verifyErr := a.readCodexAccount(ctx, false)
		if verifyErr != nil {
			return rollback(verifyErr)
		}
		finalAuth, readErr := codexauth.ReadAuthFile(store.AuthPath())
		if readErr != nil {
			return rollback(readErr)
		}
		if finalAuth.AccountFingerprint() != target.AccountFingerprint ||
			finalAuth.EmailFingerprint() != target.EmailFingerprint ||
			!finalAuth.MatchesEmail(verified.Email) {
			return rollback(codexauth.NewError(codexauth.CodeTargetMismatch, "重启后的 Codex Host 不是本地目标账号", nil))
		}
		quota, quotaErr := a.ReadCodexQuota(ctx)
		if quotaErr != nil {
			return rollback(codexauth.NewError(codexauth.CodeRuntimeUnavailable, "目标账号额度验证失败", quotaErr))
		}
		updated, updateErr := tx.ReplaceProfileSnapshot(target, finalAuth)
		if updateErr != nil {
			return rollback(updateErr)
		}
		if metadataErr := a.setManagedCodexHostAccountIdentity(store.SocketPath(), updated); metadataErr != nil {
			return rollback(metadataErr)
		}
		if activeErr := tx.SetActive(updated.ID); activeErr != nil {
			return rollback(activeErr)
		}
		tx.SetLastSwitch(codexauth.SwitchRecord{
			ProfileID: updated.ID, Status: "external_sync_success",
			Message: "已同步本地 Codex 当前账号", At: time.Now(),
		})
		if flushErr := tx.Flush(); flushErr != nil {
			return rollback(flushErr)
		}
		available = true
		runtimeRefreshed = true
		a.markAllCodexThreadsResumeOnFirstUse()
		latest := tx.Index()
		result = CodexAccountSwitchResult{
			Previous: publicCodexAccountProfilePtr(previous),
			Current:  publicCodexAccountProfile(updated),
			Revision: latest.Revision,
			Changed:  previous == nil || previous.ID != updated.ID,
			Quota:    quota,
		}
		return nil
	})
	if err != nil {
		return CodexAccountSwitchResult{}, err
	}
	committed = result.Changed || runtimeRefreshed
	return result, nil
}
