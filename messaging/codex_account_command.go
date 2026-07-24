package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/platform"
)

const codexAccountPermissionDenied = "Codex 账号是当前 WeClaw 主机的全局设置；只有管理员私聊可以查看账号列表或切换。"

func (h *Handler) dispatchCodexAccountCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	fields := runtime.fields
	if len(fields) == 2 {
		return h.renderCodexAccountListCommand(runtime)
	}
	switch fields[2] {
	case "status":
		if len(fields) != 3 {
			return textNavigationResult("用法: /cx account status")
		}
		return h.renderCodexAccountStatusCommand(runtime)
	case "use":
		if len(fields) < 4 {
			return textNavigationResult("用法: /cx account use <id-or-label>")
		}
		if !runtime.admin || !runtime.private {
			return textNavigationResult(codexAccountPermissionDenied)
		}
		return h.useCodexAccountCommand(runtime, strings.Join(fields[3:], " "), 0)
	case "select":
		return h.selectCodexAccountCommand(runtime)
	case "confirm":
		return h.confirmCodexAccountCommand(runtime)
	default:
		return textNavigationResult(codexAccountCommandUsage())
	}
}

func (h *Handler) renderCodexAccountListCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	accountAgent, err := codexAccountAgentFromRuntime(runtime)
	if err != nil {
		return textNavigationResult(formatCodexAccountCommandError(err))
	}
	status, err := accountAgent.ListCodexAccounts(runtime.ctx)
	if err != nil {
		return textNavigationResult(formatCodexAccountCommandError(err))
	}
	if !runtime.admin || !runtime.private {
		return textNavigationResult(wechatCommandText(renderCodexAccountCurrent(status), codexAccountPermissionDenied))
	}
	text := renderCodexAccountList(status)
	if runtime.req.Platform != platform.PlatformFeishu || runtime.req.Reply == nil || !runtime.req.Reply.Capabilities().Buttons {
		return textNavigationResult(text)
	}
	choices := codexAccountSwitchChoices(status)
	if len(choices) == 0 {
		return textNavigationResult(text)
	}
	scope := feishuNavigationSnapshotScope{
		AccountID: runtime.req.AccountID, ActorUserID: runtime.actorUserID, BindingKey: runtime.bindingKey,
		AgentKind: feishuWorkspaceChoiceCodex, Section: feishuNavigationSectionAccounts,
	}
	prompt := renderCodexAccountChoicePrompt(status)
	snapshot := h.feishuNavSnapshots.issueWithPrompt(scope, choices, prompt)
	choices, page := paginateFeishuChoices(choices, 1)
	choices = appendFeishuPageNavigation(choices, "/cx", "accounts", page, snapshot)
	return choiceNavigationResult(text, feishuPaginatedPrompt(prompt, page), choices)
}

func (h *Handler) renderCodexAccountStatusCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	accountAgent, err := codexAccountAgentFromRuntime(runtime)
	if err != nil {
		return textNavigationResult(formatCodexAccountCommandError(err))
	}
	status, err := accountAgent.CurrentCodexAccount(runtime.ctx, true)
	if err != nil {
		return textNavigationResult(formatCodexAccountCommandError(err))
	}
	if !runtime.admin || !runtime.private {
		return textNavigationResult(renderCodexAccountCurrent(status))
	}
	return textNavigationResult(renderCodexAccountStatus(status))
}

func (h *Handler) selectCodexAccountCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	if !runtime.admin || !runtime.private || runtime.req.Platform != platform.PlatformFeishu {
		return textNavigationResult(codexAccountPermissionDenied)
	}
	if len(runtime.fields) != 5 {
		return textNavigationResult("账号选择卡片已过期，请重新发送 /cx account。")
	}
	revision, err := strconv.ParseUint(runtime.fields[4], 10, 64)
	if err != nil || revision == 0 {
		return textNavigationResult("账号选择卡片无效，请重新发送 /cx account。")
	}
	accountAgent, err := codexAccountAgentFromRuntime(runtime)
	if err != nil {
		return textNavigationResult(formatCodexAccountCommandError(err))
	}
	status, err := accountAgent.ListCodexAccounts(runtime.ctx)
	if err != nil {
		return textNavigationResult(formatCodexAccountCommandError(err))
	}
	if status.Store.Revision != revision {
		return textNavigationResult("Codex 账号列表已更新，请重新发送 /cx account 后再选择。")
	}
	target, ok := findCodexAccountProfile(status.Store.Profiles, runtime.fields[3])
	if !ok {
		return textNavigationResult("目标 Codex 账号已不存在，请重新发送 /cx account。")
	}
	if status.Store.Current != nil && status.Store.Current.ID == target.ID {
		return textNavigationResult("当前已经使用 Codex 账号「" + target.Label + "」。")
	}
	previousLabel := "未保存"
	if status.Store.Current != nil {
		previousLabel = status.Store.Current.Label
	}
	token := h.feishuAccountConfirms.issue(feishuCodexAccountConfirmation{
		scope: feishuCodexAccountConfirmScope{
			AccountID: runtime.req.AccountID, ActorUserID: runtime.actorUserID, RouteUserID: runtime.routeUserID,
		},
		profileID: target.ID, revision: revision, previousLabel: previousLabel, targetLabel: target.Label,
	})
	choices := []platform.Choice{
		{ID: "/cx account confirm " + token, Label: "确认切换到 " + target.Label},
		feishuNavigationChoice("/cx account status", "取消并查看当前账号"),
	}
	prompt := wechatCommandText(
		"确认切换主机级 Codex 账号？",
		"当前账号: "+previousLabel,
		"目标账号: "+target.Label+formatMaskedEmailSuffix(target.EmailMasked),
		"切换不会修改工作空间、thread 或窗口绑定，也不会自动重放消息。",
	)
	return choiceNavigationResult("等待确认。", prompt, choices)
}

func (h *Handler) confirmCodexAccountCommand(runtime codexSessionCommandRuntime) navigationCommandResult {
	if !runtime.admin || !runtime.private || runtime.req.Platform != platform.PlatformFeishu {
		return textNavigationResult(codexAccountPermissionDenied)
	}
	if len(runtime.fields) != 4 || !strings.HasPrefix(runtime.fields[3], feishuCodexAccountConfirmTokenPrefix) {
		return textNavigationResult("Codex 账号确认已失效，请重新发送 /cx account。")
	}
	token := runtime.fields[3]
	record, state := h.feishuAccountConfirms.begin(token, feishuCodexAccountConfirmScope{
		AccountID: runtime.req.AccountID, ActorUserID: runtime.actorUserID, RouteUserID: runtime.routeUserID,
	})
	switch state {
	case feishuCodexAccountConfirmInvalid:
		return textNavigationResult("Codex 账号确认已过期或不属于当前窗口，请重新发送 /cx account。")
	case feishuCodexAccountConfirmRunning:
		return textNavigationResult("Codex 账号切换正在处理，请勿重复点击。")
	case feishuCodexAccountConfirmCompleted:
		if record.result == "" {
			return textNavigationResult("Codex 账号切换已经处理，请发送 /cx account status 查看当前状态。")
		}
		return textNavigationResult(record.result)
	}
	progressCtx, finishProgress := codexAccountSwitchProgressContext(runtime.ctx, runtime.req.Reply)
	runtime.ctx = progressCtx
	result := h.useCodexAccountCommand(runtime, string(record.profileID), record.revision)
	finishProgress()
	h.feishuAccountConfirms.complete(token, result.Reply)
	return result
}

func codexAccountSwitchProgressContext(ctx context.Context, reply platform.Replier) (context.Context, func()) {
	if reply == nil {
		return ctx, func() {}
	}
	phases := make(chan agent.CodexAccountSwitchPhase, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for phase := range phases {
			text := codexAccountSwitchPhaseText(phase)
			if text == "" {
				continue
			}
			if err := reply.SendText(ctx, text); err != nil {
				log.Printf("[codex-account] failed to update switch progress: phase=%s err=%v", phase, err)
			}
		}
	}()
	progressCtx := agent.WithCodexAccountSwitchProgress(ctx, func(phase agent.CodexAccountSwitchPhase) {
		select {
		case phases <- phase:
		default:
		}
	})
	return progressCtx, func() {
		close(phases)
		<-done
	}
}

func codexAccountSwitchPhaseText(phase agent.CodexAccountSwitchPhase) string {
	switch phase {
	case agent.CodexAccountSwitchChecking:
		return "Codex 账号切换：正在检查任务、writer lease 和全部 thread 状态。"
	case agent.CodexAccountSwitchSwitching:
		return "Codex 账号切换：检查通过，正在重启共享 Host 并投影目标凭据。"
	case agent.CodexAccountSwitchVerifying:
		return "Codex 账号切换：目标 Host 已启动，正在核对账号身份和额度。"
	case agent.CodexAccountSwitchRollback:
		return "Codex 账号切换：目标账号验证失败，正在恢复原账号和原运行时。"
	default:
		return ""
	}
}

func (h *Handler) useCodexAccountCommand(runtime codexSessionCommandRuntime, reference string, expectedRevision uint64) navigationCommandResult {
	result, err := h.UseCodexAccount(runtime.ctx, reference, expectedRevision)
	if err != nil {
		return textNavigationResult(formatCodexAccountCommandError(err))
	}
	previous := "未保存"
	if result.Previous != nil {
		previous = result.Previous.Label
	}
	status := "Codex 账号切换成功"
	if !result.Changed {
		status = "当前已经是目标 Codex 账号"
	}
	return textNavigationResult(wechatCommandText(
		status,
		"账号: "+previous+" → "+result.Current.Label+formatMaskedEmailSuffix(result.Current.EmailMasked),
		compactCodexQuotaSummary(result.Quota),
		"原工作空间、thread 和窗口绑定保持不变；下一条消息使用当前账号。",
	))
}

func codexAccountAgentFromRuntime(runtime codexSessionCommandRuntime) (agent.CodexAccountAgent, error) {
	accountAgent, ok := runtime.agent.(agent.CodexAccountAgent)
	if !ok {
		return nil, codexauth.NewError(codexauth.CodeRuntimeUnavailable, "当前 Codex Agent 不支持多账号管理", nil)
	}
	return accountAgent, nil
}

func renderCodexAccountList(status agent.CodexAccountStatus) string {
	lines := []string{renderCodexAccountCurrent(status)}
	if len(status.Store.Profiles) == 0 {
		return wechatCommandText(lines[0], "尚未保存账号。请在本机执行 weclaw codex account save <标签>。")
	}
	lines = append(lines, fmt.Sprintf("已保存账号: %d 个", len(status.Store.Profiles)))
	if status.Store.PendingSecretDeletes > 0 {
		lines = append(lines, fmt.Sprintf("安全提醒: %d 个旧凭据等待清理，请在本机运行 account doctor", status.Store.PendingSecretDeletes))
	}
	currentID := codexEffectiveAccountProfileID(status)
	for _, profile := range status.Store.Profiles {
		marker := "- "
		if currentID != "" && profile.ID == currentID {
			marker = "- 当前: "
		}
		lines = append(lines, marker+profile.Label+formatMaskedEmailSuffix(profile.EmailMasked)+" ["+string(profile.SecretBackend)+"]")
	}
	lines = append(lines, "切换命令: /cx account use <标签>")
	return wechatCommandText(lines...)
}

func renderCodexAccountCurrent(status agent.CodexAccountStatus) string {
	return "当前 Codex 账号: " + compactCodexAccountIdentity(status)
}

func compactCodexAccountIdentity(status agent.CodexAccountStatus) string {
	current := status.Store.Current
	auth := status.Sync.AuthProfile
	switch status.Sync.State {
	case agent.CodexAccountSyncPending:
		if auth == nil {
			return "等待自动同步"
		}
		if current == nil || current.ID == auth.ID {
			return auth.Label + "（待自动同步）"
		}
		return current.Label + " → " + auth.Label + "（待自动同步）"
	case agent.CodexAccountSyncUnsaved:
		return "本地账号未保存"
	case agent.CodexAccountSyncRuntimeMismatch:
		if auth != nil {
			return auth.Label + "（运行账号不一致）"
		}
		return "运行账号不一致"
	case agent.CodexAccountSyncRuntimeUnavailable:
		if auth != nil {
			return auth.Label + "（运行账号未确认）"
		}
		return "运行账号未确认"
	case agent.CodexAccountSyncSynced:
		if auth != nil {
			return auth.Label + formatMaskedEmailSuffix(auth.EmailMasked)
		}
	}
	if current == nil {
		return "未保存 profile"
	}
	return current.Label + formatMaskedEmailSuffix(current.EmailMasked)
}

func renderCodexAccountChoicePrompt(status agent.CodexAccountStatus) string {
	return wechatCommandText(
		"Codex 账号",
		"当前账号: "+compactCodexAccountIdentity(status),
		"可切换账号显示为按钮；选择后还需要再次确认。",
	)
}

func renderCodexAccountStatus(status agent.CodexAccountStatus) string {
	lines := []string{renderCodexAccountCurrent(status)}
	if current := codexEffectiveAccountProfile(status); current != nil {
		lines = append(lines, "凭据后端: "+string(current.SecretBackend))
	}
	if status.Host.Managed && status.Host.Running {
		lines = append(lines, fmt.Sprintf("共享 Host: 受管、运行中（generation %d）", status.Host.Generation))
	} else {
		lines = append(lines, "共享 Host: 暂不可安全切换")
	}
	if status.Store.LastSwitch != nil {
		lines = append(lines, "最近切换: "+codexAccountSwitchStatusLabel(status.Store.LastSwitch.Status))
	}
	if status.Sync.State != "" &&
		status.Sync.State != agent.CodexAccountSyncUnmanaged &&
		status.Sync.State != agent.CodexAccountSyncSynced {
		lines = append(lines, "账号同步: "+status.Sync.Message)
	}
	if status.Store.PendingSecretDeletes > 0 {
		lines = append(lines, fmt.Sprintf("旧凭据待清理: %d 个（请在本机运行 account doctor）", status.Store.PendingSecretDeletes))
	}
	if status.Quota != nil {
		lines = append(lines, compactCodexQuotaSummary(*status.Quota))
	}
	return wechatCommandText(lines...)
}

func codexAccountSwitchChoices(status agent.CodexAccountStatus) []platform.Choice {
	choices := make([]platform.Choice, 0, len(status.Store.Profiles))
	currentID := codexEffectiveAccountProfileID(status)
	for _, profile := range status.Store.Profiles {
		if currentID != "" && currentID == profile.ID {
			continue
		}
		choices = append(choices, platform.Choice{
			ID:    fmt.Sprintf("/cx account select %s %d", profile.ID, status.Store.Revision),
			Label: profile.Label + formatMaskedEmailSuffix(profile.EmailMasked),
		})
	}
	return choices
}

func codexEffectiveAccountProfileID(status agent.CodexAccountStatus) agent.CodexAccountProfileID {
	if current := codexEffectiveAccountProfile(status); current != nil {
		return current.ID
	}
	return ""
}

func codexEffectiveAccountProfile(status agent.CodexAccountStatus) *agent.CodexAccountProfile {
	if status.Sync.AuthProfile != nil &&
		(status.Sync.State == agent.CodexAccountSyncPending || status.Sync.State == agent.CodexAccountSyncSynced) {
		return status.Sync.AuthProfile
	}
	return status.Store.Current
}

func findCodexAccountProfile(profiles []agent.CodexAccountProfile, id string) (agent.CodexAccountProfile, bool) {
	for _, profile := range profiles {
		if string(profile.ID) == strings.TrimSpace(id) {
			return profile, true
		}
	}
	return agent.CodexAccountProfile{}, false
}

func compactCodexQuotaSummary(quota agent.CodexQuota) string {
	if len(quota.Limits) == 0 {
		return "额度摘要: 未返回"
	}
	parts := make([]string, 0, len(quota.Limits))
	for _, limit := range quota.Limits {
		label := codexQuotaLimitLabel(limit)
		windows := make([]string, 0, 2)
		if limit.Primary != nil {
			windows = append(windows, fmt.Sprintf("主窗口已用 %d%%", limit.Primary.UsedPercent))
		}
		if limit.Secondary != nil {
			windows = append(windows, fmt.Sprintf("次窗口已用 %d%%", limit.Secondary.UsedPercent))
		}
		if len(windows) == 0 {
			windows = append(windows, "未返回窗口")
		}
		parts = append(parts, label+" "+strings.Join(windows, "，"))
	}
	return "额度摘要: " + strings.Join(parts, "；")
}

func formatMaskedEmailSuffix(masked string) string {
	masked = strings.TrimSpace(masked)
	if masked == "" {
		return ""
	}
	return "（" + masked + "）"
}

func codexAccountSwitchStatusLabel(status string) string {
	switch strings.TrimSpace(status) {
	case "success":
		return "成功"
	case "unchanged":
		return "目标与当前账号相同"
	case "rolled_back":
		return "失败，已恢复旧账号"
	case "external_sync_success":
		return "已自动同步本地账号"
	case "external_sync_rolled_back":
		return "自动同步失败，已恢复旧账号"
	case "external_syncing":
		return "正在自动同步"
	case "rollback_failed":
		return "回滚失败，已禁止写入"
	default:
		return "未知"
	}
}

func formatCodexAccountCommandError(err error) string {
	var accountErr *codexauth.Error
	if !errors.As(err, &accountErr) {
		return "Codex 账号操作失败，请检查本机日志。"
	}
	return fmt.Sprintf("Codex 账号操作失败（%s）: %s", accountErr.Code, accountErr.Error())
}

func codexAccountCommandUsage() string {
	return wechatCommandText(
		"Codex 账号命令:",
		"/cx account 查看当前账号；管理员私聊可查看账号列表",
		"/cx account status 查看当前账号、Host、最近切换和额度摘要",
		"/cx account use <id-or-label> 管理员私聊切换主机级账号",
		"账号保存和删除只允许在本机 CLI 执行。",
	)
}

// renderFeishuCodexAccountPage 从首次打开的稳定快照翻页，不重新读取账号列表。
func (h *Handler) renderFeishuCodexAccountPage(req feishuCodexSessionCommandRequest, page feishuNavigationPageRequest) navigationCommandResult {
	agentName, ok := h.codexAgentName()
	if !ok || !h.isAdminMessage(req.message) || !isPrivateCodexCommandMessage(req.message, req.routeUserID) {
		return textNavigationResult(codexAccountPermissionDenied)
	}
	scope := feishuNavigationSnapshotScope{
		AccountID: req.message.AccountID, ActorUserID: req.message.UserID,
		BindingKey: codexBindingKey(req.routeUserID, agentName), AgentKind: feishuWorkspaceChoiceCodex,
		Section: feishuNavigationSectionAccounts,
	}
	snapshot := feishuNavigationSnapshotFromMessage(req.message)
	choices, prompt, loaded := h.feishuNavSnapshots.loadWithPrompt(snapshot, scope)
	if !loaded {
		return textNavigationResult("Codex 账号卡片已过期，请重新发送 /cx account。")
	}
	if prompt == "" {
		return textNavigationResult("Codex 账号卡片已过期，请重新发送 /cx account。")
	}
	choices, pageInfo := paginateFeishuChoices(choices, page.Page)
	choices = appendFeishuPageNavigation(choices, "/cx", "accounts", pageInfo, snapshot)
	return choiceNavigationResult("", feishuPaginatedPrompt(prompt, pageInfo), choices)
}
