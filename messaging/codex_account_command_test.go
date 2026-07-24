package messaging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/codexauth"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type fakeCodexAccountAgent struct {
	*fakeCodexThreadAgent
	mu       sync.Mutex
	status   agent.CodexAccountStatus
	quota    []bool
	useCalls int
	usedRef  string
	usedRev  uint64
	useErr   error
}

func (f *fakeCodexAccountAgent) ListCodexAccounts(context.Context) (agent.CodexAccountStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}
func (f *fakeCodexAccountAgent) CurrentCodexAccount(_ context.Context, withQuota bool) (agent.CodexAccountStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quota = append(f.quota, withQuota)
	return f.status, nil
}
func (f *fakeCodexAccountAgent) SaveCodexAccount(context.Context, agent.CodexAccountSaveOptions) (agent.CodexAccountProfile, error) {
	return agent.CodexAccountProfile{}, nil
}
func (f *fakeCodexAccountAgent) UseCodexAccount(_ context.Context, reference string, revision uint64) (agent.CodexAccountSwitchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.useCalls++
	f.usedRef, f.usedRev = reference, revision
	if f.useErr != nil {
		return agent.CodexAccountSwitchResult{}, f.useErr
	}
	target, ok := findCodexAccountProfile(f.status.Store.Profiles, reference)
	if !ok {
		return agent.CodexAccountSwitchResult{}, codexauth.NewError(codexauth.CodeNotFound, "missing", nil)
	}
	previous := f.status.Store.Current
	f.status.Store.Current = &target
	f.status.Store.Revision++
	return agent.CodexAccountSwitchResult{
		Previous: previous, Current: target, Revision: f.status.Store.Revision, Changed: previous == nil || previous.ID != target.ID,
		Quota: agent.CodexQuota{Limits: []agent.CodexRateLimit{{ID: "codex", Primary: &agent.CodexRateLimitWindow{UsedPercent: 12}}}},
	}, nil
}
func (f *fakeCodexAccountAgent) RemoveCodexAccount(context.Context, string) error { return nil }
func (f *fakeCodexAccountAgent) DoctorCodexAccounts(context.Context) codexauth.DoctorResult {
	return codexauth.DoctorResult{OK: true}
}

func (f *fakeCodexAccountAgent) quotaRequests() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.quota...)
}

func newMessagingAccountFixture(t *testing.T, profileCount int) (*Handler, *fakeCodexAccountAgent, platform.IncomingMessage) {
	t.Helper()
	profiles := make([]agent.CodexAccountProfile, 0, profileCount)
	for index := 0; index < profileCount; index++ {
		profiles = append(profiles, agent.CodexAccountProfile{
			ID:    agent.CodexAccountProfileID(fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)),
			Label: fmt.Sprintf("账号-%02d", index+1), AuthMode: "chatgpt",
			EmailMasked: fmt.Sprintf("u***%d@example.com", index+1), SecretBackend: codexauth.SecretBackendKeyring,
		})
	}
	threadAgent := &fakeCodexThreadAgent{fakeAgent: fakeAgent{info: agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}}}
	accountAgent := &fakeCodexAccountAgent{fakeCodexThreadAgent: threadAgent}
	accountAgent.status = agent.CodexAccountStatus{
		Store: agent.CodexAccountStoreStatus{Revision: 7, Profiles: profiles, ManagedHost: true},
		Host:  agent.CodexHostStatus{Managed: true, Running: true, Generation: 3},
	}
	if len(profiles) > 0 {
		current := profiles[0]
		accountAgent.status.Store.Current = &current
	}
	h := NewHandler(nil, nil)
	h.defaultName = "codex"
	h.agents["codex"] = accountAgent
	h.SetAdminUsers([]string{"on_admin"})
	msg := platform.IncomingMessage{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", UserID: "ou_admin", UserAliases: []string{"on_admin"},
		Metadata: map[string]string{feishuSessionMetadataKey: "feishu:cli_a:tenant:dm:oc_chat:ou_admin", "feishu_chat_type": "p2p"},
	}
	return h, accountAgent, msg
}

func TestFeishuCodexAccountListUsesSnapshotPagination(t *testing.T) {
	h, _, msg := newMessagingAccountFixture(t, 10)
	msg.Text, msg.MessageID = "/cx account", "account-list"
	first := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, first)
	if len(first.Choices) != 1 || len(first.Choices[0].Choices) != 8 {
		t.Fatalf("choices=%#v", first.Choices)
	}
	if !strings.Contains(first.Choices[0].Prompt, "当前账号: 账号-01（u***1@example.com）") {
		t.Fatalf("prompt=%q, want current account as text", first.Choices[0].Prompt)
	}
	for _, choice := range first.Choices[0].Choices {
		if strings.Contains(choice.Label, "账号-01") {
			t.Fatalf("current account must not be rendered as a button: %#v", choice)
		}
	}
	next := first.Choices[0].Choices[7]
	if next.ID != "/cx page accounts 2" || next.Metadata[platform.ChoiceMetadataNavigationSnapshot] == "" {
		t.Fatalf("next=%#v", next)
	}

	msg.Text = ""
	msg.MessageID = "account-page-2"
	msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{
		"choice": next.ID, platform.ChoiceMetadataNavigationSnapshot: next.Metadata[platform.ChoiceMetadataNavigationSnapshot],
	}}
	second := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, second)
	if len(second.Choices) != 1 || !strings.Contains(second.Choices[0].Prompt, "第 2/2 页") {
		t.Fatalf("second=%#v", second.Choices)
	}
	if !strings.Contains(second.Choices[0].Prompt, "当前账号: 账号-01（u***1@example.com）") {
		t.Fatalf("second prompt=%q, want cached current account as text", second.Choices[0].Prompt)
	}
}

func TestFeishuCodexAccountListShowsEverySavedAccount(t *testing.T) {
	h, _, msg := newMessagingAccountFixture(t, 3)
	msg.Text, msg.MessageID = "/cx account", "account-list-all"
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), msg, reply)

	if len(reply.Choices) != 1 {
		t.Fatalf("choices=%#v", reply.Choices)
	}
	card := reply.Choices[0]
	if !strings.Contains(card.Prompt, "当前账号: 账号-01（u***1@example.com）") {
		t.Fatalf("prompt=%q, want current account text", card.Prompt)
	}
	if len(card.Choices) != 2 {
		t.Fatalf("buttons=%#v, want two switchable accounts", card.Choices)
	}
	for index, want := range []string{"账号-02（u***2@example.com）", "账号-03（u***3@example.com）"} {
		if card.Choices[index].Label != want {
			t.Fatalf("button[%d]=%#v, want %q", index, card.Choices[index], want)
		}
	}
}

func TestCodexAccountStatusReportsPendingSecretCleanup(t *testing.T) {
	_, accountAgent, _ := newMessagingAccountFixture(t, 2)
	accountAgent.status.Store.PendingSecretDeletes = 2
	got := renderCodexAccountStatus(accountAgent.status)
	if !strings.Contains(got, "旧凭据待清理: 2 个") || strings.Contains(got, "secret_ref") {
		t.Fatalf("status=%q", got)
	}
}

func TestFeishuCodexAccountSelectionRequiresScopedConfirmationAndIsIdempotent(t *testing.T) {
	h, accountAgent, msg := newMessagingAccountFixture(t, 2)
	msg.Text, msg.MessageID = "/cx account", "account-list"
	listed := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, listed)
	selectCommand := listed.Choices[0].Choices[0].ID

	msg.Text = ""
	msg.MessageID = "account-select"
	msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{"choice": selectCommand}}
	selected := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, selected)
	if len(selected.Choices) != 1 || len(selected.Choices[0].Choices) != 2 || !strings.Contains(selected.Choices[0].Prompt, "当前账号") {
		t.Fatalf("confirmation=%#v", selected.Choices)
	}
	confirmCommand := selected.Choices[0].Choices[0].ID
	if !strings.HasPrefix(confirmCommand, "/cx account confirm "+feishuCodexAccountConfirmTokenPrefix) {
		t.Fatalf("confirm command=%q", confirmCommand)
	}

	msg.MessageID = "account-confirm"
	msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{"choice": confirmCommand}}
	confirmed := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, confirmed)
	if accountAgent.useCalls != 1 || accountAgent.usedRev != 7 || !containsText(confirmed.Texts, "账号切换成功") {
		t.Fatalf("calls=%d rev=%d texts=%#v", accountAgent.useCalls, accountAgent.usedRev, confirmed.Texts)
	}

	msg.MessageID = "account-confirm-again"
	repeated := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, repeated)
	if accountAgent.useCalls != 1 || !containsText(repeated.Texts, "账号切换成功") {
		t.Fatalf("duplicate calls=%d texts=%#v", accountAgent.useCalls, repeated.Texts)
	}
}

func TestFeishuCodexAccountSwitchDeniedOutsideAdminPrivateChat(t *testing.T) {
	h, accountAgent, msg := newMessagingAccountFixture(t, 2)
	msg.Metadata["feishu_chat_type"] = "group"
	msg.Metadata[feishuSessionMetadataKey] = "feishu:cli_a:tenant:group:oc_chat"
	msg.Text, msg.MessageID = "/cx account", "account-group-list"
	listed := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, listed)
	if len(listed.Choices) != 0 || !containsText(listed.Texts, codexAccountPermissionDenied) {
		t.Fatalf("choices=%#v texts=%#v", listed.Choices, listed.Texts)
	}

	msg.Text, msg.MessageID = "/cx account use 账号-02", "account-group-use"
	used := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, used)
	if accountAgent.useCalls != 0 || !containsText(used.Texts, codexAccountPermissionDenied) {
		t.Fatalf("calls=%d texts=%#v", accountAgent.useCalls, used.Texts)
	}

	for _, testCase := range []struct {
		command string
		account string
	}{
		{command: "/cx account status", account: "当前 Codex 账号"},
		{command: "/cx status", account: "账号: 账号-01"},
	} {
		msg.Text, msg.MessageID, msg.RawCommand = testCase.command, "account-group-status-"+testCase.command, nil
		statusReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
		h.HandleMessage(context.Background(), msg, statusReply)
		joined := strings.Join(statusReply.Texts, "\n")
		if !strings.Contains(joined, testCase.account) || strings.Contains(joined, "凭据后端") || strings.Contains(joined, "generation") {
			t.Fatalf("command=%q texts=%#v", testCase.command, statusReply.Texts)
		}
	}
}

func TestCodexStatusDoesNotFetchQuotaOrExposeAccountDetails(t *testing.T) {
	h, accountAgent, msg := newMessagingAccountFixture(t, 2)
	msg.Text, msg.MessageID = "/cx status", "compact-account-status"
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})

	h.HandleMessage(context.Background(), msg, reply)

	joined := strings.Join(reply.Texts, "\n")
	if !strings.Contains(joined, "账号: 账号-01") {
		t.Fatalf("texts=%#v, want compact account label", reply.Texts)
	}
	for _, detail := range []string{"u***1@example.com", "凭据后端", "共享 Host", "generation", "最近切换", "额度"} {
		if strings.Contains(joined, detail) {
			t.Fatalf("texts=%#v, should omit %q", reply.Texts, detail)
		}
	}
	requests := accountAgent.quotaRequests()
	if len(requests) == 0 || requests[len(requests)-1] {
		t.Fatalf("quota requests=%#v, /cx status must not fetch quota", requests)
	}
}

func TestCodexAccountSwitchRejectsHandlerActiveTaskBeforeAgentMutation(t *testing.T) {
	h, accountAgent, msg := newMessagingAccountFixture(t, 2)
	task, _, started := h.beginActiveTask(context.Background(), "codex-account-active", activeTaskMeta{
		owner: msg.UserID, agentName: "codex", message: "running",
	})
	if !started {
		t.Fatal("failed to create active Codex task")
	}
	defer h.finishActiveTask("codex-account-active", task)
	msg.Text, msg.MessageID = "/cx account use 账号-02", "account-active-use"
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, reply)
	if accountAgent.useCalls != 0 || !containsText(reply.Texts, "还有 1 个 Codex 任务") {
		t.Fatalf("calls=%d texts=%#v", accountAgent.useCalls, reply.Texts)
	}
}

func TestFeishuCodexAccountOldRevisionAndExpiredConfirmationAreRejected(t *testing.T) {
	h, accountAgent, msg := newMessagingAccountFixture(t, 2)
	msg.Text, msg.MessageID = "/cx account", "account-list-old"
	listed := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, listed)
	selectCommand := listed.Choices[0].Choices[0].ID
	accountAgent.mu.Lock()
	accountAgent.status.Store.Revision++
	accountAgent.mu.Unlock()
	msg.Text, msg.MessageID = "", "account-select-old"
	msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{"choice": selectCommand}}
	stale := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, stale)
	if !containsText(stale.Texts, "账号列表已更新") {
		t.Fatalf("texts=%#v", stale.Texts)
	}

	accountAgent.mu.Lock()
	accountAgent.status.Store.Revision = 7
	accountAgent.mu.Unlock()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	h.feishuAccountConfirms.now = func() time.Time { return now }
	msg.MessageID = "account-select-fresh"
	selected := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, selected)
	confirmCommand := selected.Choices[0].Choices[0].ID
	now = now.Add(feishuCodexAccountConfirmTTL + time.Second)
	msg.MessageID = "account-confirm-expired"
	msg.RawCommand = &platform.CardAction{Action: "choice", Value: map[string]string{"choice": confirmCommand}}
	expired := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), msg, expired)
	if accountAgent.useCalls != 0 || !containsText(expired.Texts, "已过期") {
		t.Fatalf("calls=%d texts=%#v", accountAgent.useCalls, expired.Texts)
	}
}

func TestCodexAccountConfirmationStoreBindsScopeAndDeduplicates(t *testing.T) {
	store := feishuCodexAccountConfirmStore{}
	scope := feishuCodexAccountConfirmScope{AccountID: "app", ActorUserID: "actor", RouteUserID: "route"}
	token := store.issue(feishuCodexAccountConfirmation{scope: scope, profileID: "profile", revision: 2})
	if _, state := store.begin(token, feishuCodexAccountConfirmScope{AccountID: "app", ActorUserID: "other", RouteUserID: "route"}); state != feishuCodexAccountConfirmInvalid {
		t.Fatalf("wrong actor state=%v", state)
	}
	if _, state := store.begin(token, scope); state != feishuCodexAccountConfirmStarted {
		t.Fatalf("first state=%v", state)
	}
	if _, state := store.begin(token, scope); state != feishuCodexAccountConfirmRunning {
		t.Fatalf("running state=%v", state)
	}
	store.complete(token, "完成")
	record, state := store.begin(token, scope)
	if state != feishuCodexAccountConfirmCompleted || record.result != "完成" {
		t.Fatalf("completed state=%v record=%#v", state, record)
	}
}

func TestPlatformMessageLogRedactsCodexAccountConfirmationToken(t *testing.T) {
	if got := platformMessageLogText("/cx account confirm @acct_secret"); got != "/cx account confirm <redacted>" {
		t.Fatalf("log text=%q", got)
	}
}
