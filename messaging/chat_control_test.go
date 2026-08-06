package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

func TestModeCommandTogglesYolo(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"

	if got := h.handleModeCommand(user, "/mode"); !strings.Contains(got, "default") {
		t.Fatalf("default mode expected, got %q", got)
	}
	if got := h.handleModeCommand(user, "/mode yolo"); !strings.Contains(got, "yolo") {
		t.Fatalf("switch to yolo expected, got %q", got)
	}
	if !h.isYoloMode(user) {
		t.Fatal("expected yolo mode active")
	}
	if got := h.handleModeCommand(user, "/mode default"); !strings.Contains(got, "default") {
		t.Fatalf("switch to default expected, got %q", got)
	}
	if h.isYoloMode(user) {
		t.Fatal("expected yolo mode cleared")
	}
	if got := h.handleModeCommand(user, "/mode bogus"); !strings.Contains(got, "用法") {
		t.Fatalf("usage hint expected for unknown subcommand, got %q", got)
	}
}

func TestModeYoloIsolatedPerUser(t *testing.T) {
	h := NewHandler(nil, nil)
	h.setYoloMode("wechat:u1", true)
	if h.isYoloMode("wechat:u2") {
		t.Fatal("yolo must not leak across users")
	}
}

// TestModeYoloIsolatedPerFeishuSession 验证同一用户的不同飞书窗口互不共享审批模式。
func TestModeYoloIsolatedPerFeishuSession(t *testing.T) {
	h := NewHandler(nil, nil)
	routeA := "feishu:tenant:dm:chat-a:ou_user"
	routeB := "feishu:tenant:group:chat-b"
	h.HandleMessage(context.Background(), modeCommandMessage("mode-a", routeA, "/mode yolo"), platformtest.NewReplier(platform.Capabilities{Text: true}))
	replyB := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), modeCommandMessage("mode-b", routeB, "/mode"), replyB)

	modeA := approvalModeKey("ou_user", routeA)
	modeB := approvalModeKey("ou_user", routeB)
	if !h.isYoloMode(modeA) || h.isYoloMode(modeB) {
		t.Fatalf("routeA=%t routeB=%t，期望审批模式按飞书窗口隔离", h.isYoloMode(modeA), h.isYoloMode(modeB))
	}
	if !containsText(replyB.Texts, "default") {
		t.Fatalf("窗口 B 回复=%#v，期望保持 default", replyB.Texts)
	}
	if !strings.Contains(h.buildStatusForRoute("ou_user", routeA, platform.PlatformFeishu, "cli_main"), "mode: yolo") {
		t.Fatal("窗口 A 状态应显示 yolo")
	}
	if !strings.Contains(h.buildStatusForRoute("ou_user", routeB, platform.PlatformFeishu, "cli_main"), "mode: default") {
		t.Fatal("窗口 B 状态应显示 default")
	}
}

func TestFeishuModeCommandUsesSessionScopedChoiceCard(t *testing.T) {
	h := NewHandler(nil, nil)
	route := "feishu:tenant:dm:chat-a:ou_user"
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), modeCommandMessage("mode-card", route, "/mode"), reply)

	if len(reply.Texts) != 0 || len(reply.Choices) != 1 {
		t.Fatalf("texts=%#v choices=%#v，期望飞书 /mode 只发送一张选择卡", reply.Texts, reply.Choices)
	}
	card := reply.Choices[0]
	if !strings.Contains(card.Prompt, "default") || len(card.Choices) != 2 {
		t.Fatalf("card=%#v，期望显示当前模式和两个选项", card)
	}
	if card.Choices[0].ID != "/mode default" || !strings.Contains(card.Choices[0].Label, "当前") ||
		card.Choices[1].ID != "/mode yolo" {
		t.Fatalf("choices=%#v，期望 default/yolo 且标记当前项", card.Choices)
	}
	for _, choice := range card.Choices {
		if choice.Metadata[feishuSessionMetadataKey] != route {
			t.Fatalf("choice=%#v，期望透传飞书窗口路由", choice)
		}
	}
}

func TestFeishuModeCardChoiceReusesOriginalSessionRoute(t *testing.T) {
	h := NewHandler(nil, nil)
	route := "feishu:tenant:group:chat-b"
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_actor", MessageID: "mode-choice",
		RawCommand: &platform.CardAction{Action: "choice", Value: map[string]string{"choice": "/mode yolo"}},
		Metadata:   map[string]string{feishuSessionMetadataKey: route},
	}, reply)

	modeKey := approvalModeKey("ou_actor", route)
	if !h.isYoloMode(modeKey) || h.isYoloMode(route) {
		t.Fatalf("actor route=%t shared route=%t，卡片选择必须只写入当前操作者", h.isYoloMode(modeKey), h.isYoloMode(route))
	}
	if len(reply.Choices) != 0 || !containsText(reply.Texts, "已切换为 yolo") {
		t.Fatalf("texts=%#v choices=%#v，卡片回放应复用文本切换结果", reply.Texts, reply.Choices)
	}
}

func TestGroupModeYoloIsolatedPerActor(t *testing.T) {
	h := NewHandler(nil, nil)
	route := "feishu:tenant:group:chat-b"
	replyA := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_actor_a", MessageID: "mode-a", Text: "/mode yolo",
		Route: platform.SessionRoute{Key: route},
	}, replyA)

	if !h.isYoloMode(approvalModeKey("ou_actor_a", route)) {
		t.Fatal("actor A should have yolo enabled")
	}
	if h.isYoloMode(approvalModeKey("ou_actor_b", route)) {
		t.Fatal("actor B must not inherit actor A yolo mode in the same group")
	}
	options := []agent.ApprovalOption{{ID: "deny", Kind: "deny"}, {ID: "allow", Kind: "allow"}}
	replyB := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h.approvalHandlerForUser("ou_actor_b", route, replyB)(ctx, agent.ApprovalRequest{Options: options})
	if len(replyB.Choices) != 1 {
		t.Fatalf("actor B choices=%#v, want explicit approval card", replyB.Choices)
	}
}

func TestModeCommandWithoutFeishuButtonsFallsBackToText(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	h.HandleMessage(context.Background(), modeCommandMessage("mode-text", "feishu:tenant:dm:chat-a:ou_user", "/mode"), reply)

	if len(reply.Choices) != 0 || !containsText(reply.Texts, "default") {
		t.Fatalf("texts=%#v choices=%#v，缺少按钮能力时应回退文本", reply.Texts, reply.Choices)
	}
}

// modeCommandMessage 构造指定飞书窗口的审批模式命令。
func modeCommandMessage(messageID string, route string, text string) platform.IncomingMessage {
	return platform.IncomingMessage{
		Platform: platform.PlatformFeishu, UserID: "ou_user", MessageID: messageID, Text: text,
		Metadata: map[string]string{feishuSessionMetadataKey: route},
	}
}

func TestApprovalHandlerYoloAutoApproves(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"
	h.setYoloMode(user, true)

	denyReply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	handler := h.approvalHandlerForUser(user, user, denyReply)
	options := []agent.ApprovalOption{
		{ID: "deny-1", Kind: "deny"},
		{ID: "allow-1", Kind: "allow"},
	}
	decision, err := handler(context.Background(), agent.ApprovalRequest{Options: options})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "allow-1" {
		t.Fatalf("expected allow option auto-approved, got %q", decision)
	}
	if len(denyReply.Choices) != 0 {
		t.Fatalf("yolo mode must not prompt buttons, got %d AskChoices calls", len(denyReply.Choices))
	}
}

func TestAutoApproveApprovalOptionRequiresExplicitAllow(t *testing.T) {
	options := []agent.ApprovalOption{
		{ID: "deny-1", Kind: "deny"},
		{ID: "cancel-1", Kind: "cancel"},
	}
	if got := autoApproveApprovalOption(options); got != "" {
		t.Fatalf("autoApproveApprovalOption=%q, want empty without allow option", got)
	}
}

// TestApprovalHandlerReadsRouteMode 验证审批只读取任务所属窗口的模式。
func TestApprovalHandlerReadsRouteMode(t *testing.T) {
	h := NewHandler(nil, nil)
	routeA := "feishu:tenant:dm:chat-a:ou_user"
	routeB := "feishu:tenant:group:chat-b"
	h.setYoloMode(approvalModeKey("ou_user", routeA), true)
	options := []agent.ApprovalOption{{ID: "deny-1", Kind: "deny"}, {ID: "allow-1", Kind: "allow"}}

	replyA := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	ctxA, cancelA := context.WithTimeout(context.Background(), taskQueueProbeDelay)
	defer cancelA()
	decision, err := h.approvalHandlerForUser("ou_user", routeA, replyA)(ctxA, agent.ApprovalRequest{Options: options})
	if err != nil || decision != "allow-1" || len(replyA.Choices) != 0 {
		t.Fatalf("窗口 A decision=%q err=%v choices=%#v，期望自动同意", decision, err, replyA.Choices)
	}
	replyB := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h.approvalHandlerForUser("ou_user", routeB, replyB)(ctx, agent.ApprovalRequest{Options: options})
	if len(replyB.Choices) != 1 {
		t.Fatalf("窗口 B choices=%#v，期望继续按钮确认", replyB.Choices)
	}
}

func TestApprovalHandlerYoloAutoApprovesCodexFileChangeDecision(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"
	h.setYoloMode(user, true)

	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Buttons: true})
	handler := h.approvalHandlerForUser(user, user, reply)
	options := []agent.ApprovalOption{
		{ID: "accept", Kind: "allow"},
		{ID: "cancel", Kind: "deny"},
	}
	decision, err := handler(context.Background(), agent.ApprovalRequest{Options: options})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "accept" {
		t.Fatalf("expected accept decision auto-approved, got %q", decision)
	}
	if len(reply.Choices) != 0 {
		t.Fatalf("yolo mode must not prompt buttons, got %d AskChoices calls", len(reply.Choices))
	}
}

func TestApprovalHandlerYoloRecordsAutomaticApprovalWithoutSendingApprovalCard(t *testing.T) {
	h := NewHandler(nil, nil)
	route := "feishu:tenant:dm:chat-a:ou_user"
	h.setYoloMode(approvalModeKey("ou_user", route), true)
	reply := newAutomaticApprovalCaptureReplier("card-task-1")
	serialized := newSerializedReplier(reply)

	decision, err := h.approvalHandlerForRoute(agentInteractionContextOptions{
		actorUserID: "ou_user", routeUserID: route, agentName: "claude", reply: serialized,
	})(context.Background(), agent.ApprovalRequest{
		RequestID: "request-1",
		ToolCall:  json.RawMessage(`{"command":"date"}`),
		Options: []agent.ApprovalOption{
			{ID: "deny", Name: "拒绝", Kind: "deny"},
			{ID: "allow_always", Name: "始终允许", Kind: "allow"},
		},
	})
	if err != nil || decision != "allow_always" {
		t.Fatalf("decision=%q err=%v，期望 YOLO 保持自动放行", decision, err)
	}
	if len(reply.choiceCh) != 0 {
		t.Fatal("YOLO 后续审批不应发送新的审批卡片")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	record := reply.waitAutomaticApproval(t, ctx)
	if !strings.Contains(record.prompt, `{"command":"date"}`) {
		t.Fatalf("prompt=%q，期望保留审批摘要供任务卡记录", record.prompt)
	}
	if record.choice.ID != "allow_always" || record.choice.Label != "始终允许" ||
		record.choice.Metadata["approval_key"] == "" ||
		record.choice.Metadata["task_card_id"] != "card-task-1" {
		t.Fatalf("choice=%#v，期望记录真实允许选项及任务卡关联", record.choice)
	}
}

func TestApprovalHandlerYoloDoesNotWaitForAutomaticCardUpdate(t *testing.T) {
	h := NewHandler(nil, nil)
	route := "feishu:tenant:dm:chat-a:ou_user"
	h.setYoloMode(approvalModeKey("ou_user", route), true)
	reply := newBlockingAutomaticApprovalCaptureReplier("card-task-1")
	defer close(reply.release)
	result := make(chan string, 1)
	go func() {
		decision, err := h.approvalHandlerForRoute(agentInteractionContextOptions{
			actorUserID: "ou_user", routeUserID: route, agentName: "claude", reply: reply,
		})(context.Background(), agent.ApprovalRequest{
			Options: []agent.ApprovalOption{{ID: "allow", Kind: "allow"}},
		})
		if err != nil {
			result <- "error:" + err.Error()
			return
		}
		result <- decision
	}()

	select {
	case <-reply.started:
	case <-time.After(time.Second):
		t.Fatal("automatic card update did not start")
	}
	select {
	case got := <-result:
		if got != "allow" {
			t.Fatalf("decision=%q，期望 YOLO 立即返回允许决策", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("YOLO 真实审批被飞书卡片更新阻塞")
	}
}

func TestModeYoloResolvesExistingClaudeApprovalForSameRoute(t *testing.T) {
	h := NewHandler(nil, nil)
	route := "feishu:tenant:dm:chat-a:ou_user"
	reply := newAutomaticApprovalCaptureReplier("card-task-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan string, 1)
	go func() {
		decision, err := h.approvalHandlerForRoute(agentInteractionContextOptions{
			actorUserID: "ou_user", routeUserID: route, agentName: "claude", reply: reply,
		})(ctx, agent.ApprovalRequest{
			Options: []agent.ApprovalOption{
				{ID: "deny", Kind: "deny"},
				{ID: "allow_always", Kind: "allow"},
			},
		})
		if err != nil {
			result <- "error:" + err.Error()
			return
		}
		result <- decision
	}()
	card := reply.waitChoiceRequest(t, ctx)
	if !strings.HasPrefix(card.prompt, "Claude 请求执行敏感操作") {
		t.Fatalf("prompt=%q，授权卡应标明 Claude 来源", card.prompt)
	}
	if got := card.choices[0].Metadata[platform.ChoiceMetadataInteractionKind]; got != platform.ChoiceInteractionApproval {
		t.Fatalf("interaction kind=%q，期望 approval", got)
	}
	if got := card.choices[0].Metadata[platform.ChoiceMetadataAgentName]; got != "Claude" {
		t.Fatalf("agent name=%q，期望 Claude", got)
	}

	modeReply := h.handleModeCommandForActor(route, "ou_user", "/mode yolo")
	if !strings.Contains(modeReply, "放行 1 个") {
		t.Fatalf("mode reply=%q，期望说明已放行待确认授权", modeReply)
	}
	record := reply.waitAutomaticApproval(t, ctx)
	if record.choice.ID != "allow_always" || record.choice.Metadata["approval_key"] == "" {
		t.Fatalf("automatic record=%#v，期望把既有审批收敛为 YOLO 允许记录", record)
	}
	select {
	case got := <-result:
		if got != "allow_always" {
			t.Fatalf("decision=%q，期望放行 Claude 授权", got)
		}
	case <-ctx.Done():
		t.Fatal("切换 yolo 后待确认授权未被唤醒")
	}
}

func TestModeYoloReportsCardUpdateFailureWithoutRevertingApproval(t *testing.T) {
	h := NewHandler(nil, nil)
	recorder := &recordingAuditLogger{}
	h.SetAuditLogger(recorder)
	route := "feishu:tenant:dm:chat-a:ou_user"
	reply := newAutomaticApprovalCaptureReplier("card-task-1")
	reply.recordErr = context.DeadlineExceeded
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan string, 1)
	go func() {
		decision, err := h.approvalHandlerForRoute(agentInteractionContextOptions{
			actorUserID: "ou_user", routeUserID: route, agentName: "claude", reply: reply,
		})(ctx, agent.ApprovalRequest{
			RequestID: "request-2",
			ToolCall:  json.RawMessage(`{"command":"sensitive-command"}`),
			Options: []agent.ApprovalOption{
				{ID: "deny", Kind: "deny"},
				{ID: "allow_always", Kind: "allow"},
			},
		})
		if err != nil {
			result <- "error:" + err.Error()
			return
		}
		result <- decision
	}()
	reply.waitChoiceRequest(t, ctx)

	modeReply := h.handleModeCommandForActor(route, "ou_user", "/mode yolo")
	if !strings.Contains(modeReply, "放行 1 个") || !strings.Contains(modeReply, "卡片更新失败 1 个") {
		t.Fatalf("mode reply=%q，期望同时报告真实放行与展示失败", modeReply)
	}
	entries := recorder.snapshot()
	if !auditEntriesContain(entries, "approval_auto_yolo_display_failed", "reason=card_update_timeout") {
		t.Fatalf("audit entries=%#v，期望记录脱敏的卡片失败原因", entries)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Summary, "sensitive-command") {
			t.Fatalf("audit entry leaked approval command: %#v", entry)
		}
	}
	select {
	case got := <-result:
		if got != "allow_always" {
			t.Fatalf("decision=%q，卡片失败不得回滚真实审批", got)
		}
	case <-ctx.Done():
		t.Fatal("卡片更新失败后审批请求未被唤醒")
	}
}

func TestModeYoloDecisionWinsWhenOriginalApprovalCardSendFails(t *testing.T) {
	h := NewHandler(nil, nil)
	route := "feishu:tenant:dm:chat-a:ou_user"
	reply := newFailingChoiceAutomaticApprovalCaptureReplier("card-task-1", errors.New("send approval card"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan string, 1)
	go func() {
		decision, err := h.approvalHandlerForRoute(agentInteractionContextOptions{
			actorUserID: "ou_user", routeUserID: route, agentName: "claude", reply: reply,
		})(ctx, agent.ApprovalRequest{
			Options: []agent.ApprovalOption{{ID: "deny", Kind: "deny"}, {ID: "allow", Kind: "allow"}},
		})
		if err != nil {
			result <- "error:" + err.Error()
			return
		}
		result <- decision
	}()
	reply.waitChoiceRequest(t, ctx)
	modeResult := make(chan string, 1)
	go func() { modeResult <- h.handleModeCommandForActor(route, "ou_user", "/mode yolo") }()
	waitForResolvedApprovalForTest(t, ctx, h)
	close(reply.release)

	select {
	case got := <-result:
		if got != "allow" {
			t.Fatalf("decision=%q，卡片发送失败不得覆盖已经提交的 YOLO 允许决策", got)
		}
	case <-ctx.Done():
		t.Fatal("waiting for approval result timed out")
	}
	select {
	case modeReply := <-modeResult:
		if !strings.Contains(modeReply, "放行 1 个") {
			t.Fatalf("mode reply=%q，期望保留真实放行结果", modeReply)
		}
	case <-ctx.Done():
		t.Fatal("waiting for mode result timed out")
	}
}

func waitForResolvedApprovalForTest(t *testing.T, ctx context.Context, h *Handler) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		h.pendingApprovalsMu.Lock()
		resolved := false
		for _, pending := range h.pendingApprovals {
			resolved = resolved || pending.resolved.Load()
		}
		h.pendingApprovalsMu.Unlock()
		if resolved {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("pending approval was not resolved")
		}
	}
}

func TestApprovalAllowAlwaysWithoutNameDoesNotPanic(t *testing.T) {
	if got := approvalChoiceLabel(agent.ApprovalOption{ID: "allow_always", Kind: "allow"}); got != "始终允许" {
		t.Fatalf("label=%q，期望空名称的 allow_always 安全回退", got)
	}
}

func TestListActiveTasksEmptyAndPopulated(t *testing.T) {
	h := NewHandler(nil, nil)
	user := "wechat:u1"
	if got := h.handleListActiveTasks(user); !strings.Contains(got, "没有运行中的任务") {
		t.Fatalf("expected empty message, got %q", got)
	}

	_, _, started := h.beginActiveTask(context.Background(), "key-1", activeTaskMeta{
		owner:     user,
		agentName: "codex",
		message:   "重构登录模块",
	})
	if !started {
		t.Fatal("expected active task to start")
	}
	got := h.handleListActiveTasks(user)
	if !strings.Contains(got, "codex") || !strings.Contains(got, "重构登录模块") {
		t.Fatalf("expected running codex task listed, got %q", got)
	}
	task, ok := h.activeTask("key-1")
	if !ok {
		t.Fatal("expected active task to exist")
	}
	task.recordProgressText(time.Now(), "正在修改表单组件")
	got = h.handleListActiveTasks(user)
	if !strings.Contains(got, "最近进展") || !strings.Contains(got, "正在修改表单组件") {
		t.Fatalf("expected active task progress listed, got %q", got)
	}
	if !strings.Contains(got, "/stop") || strings.Contains(got, "/cancel 停止当前任务") {
		t.Fatalf("expected /stop guidance, got %q", got)
	}
	if other := h.handleListActiveTasks("wechat:u2"); !strings.Contains(other, "没有运行中的任务") {
		t.Fatalf("tasks must be scoped per owner, got %q", other)
	}
	if !task.claimTerminal() {
		t.Fatal("expected task terminal claim")
	}
	if got := h.handleListActiveTasks(user); !strings.Contains(got, "没有运行中的任务") {
		t.Fatalf("terminal task must not remain in /ps, got %q", got)
	}
	if got := h.ActiveTaskCount(); got != 0 {
		t.Fatalf("ActiveTaskCount=%d, want terminal task excluded", got)
	}
}

func TestRunningTasksFooterDoesNotPromptCodexAppOperation(t *testing.T) {
	readOnly := runningTasksFooter([]runningTaskView{{stoppable: false}})
	if strings.Contains(readOnly, "Codex App") || strings.Contains(readOnly, "App 中操作") {
		t.Fatalf("read-only footer must not prompt app operation, got %q", readOnly)
	}
	if !strings.Contains(readOnly, "结果会自动返回当前会话") {
		t.Fatalf("read-only footer must describe result delivery, got %q", readOnly)
	}
	mixed := runningTasksFooter([]runningTaskView{{stoppable: true}, {stoppable: false}})
	if strings.Contains(mixed, "Codex App") || strings.Contains(mixed, "App 中操作") {
		t.Fatalf("mixed footer must not prompt app operation, got %q", mixed)
	}
}

func TestListInProcessUnknownCodexTaskAdvertisesStop(t *testing.T) {
	h := NewHandler(nil, nil)
	h.beginActiveTask(context.Background(), "codex-task", activeTaskMeta{
		owner: "user-1", agentName: "codex",
		runtimeOwner:  agent.CodexRuntimeUnknown,
		codexThreadID: "thread-1", codexTurnID: "turn-1",
		inProcessCodexLifecycle: true,
	})

	got := h.handleListActiveTasks("user-1")

	if !strings.Contains(got, "/stop") {
		t.Fatalf("in-process task with resolvable runtime should advertise /stop, got %q", got)
	}
}
