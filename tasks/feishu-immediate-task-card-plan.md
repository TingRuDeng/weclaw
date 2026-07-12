# 飞书即时任务卡片实施计划

> **执行要求：** 使用 `seq-execute` 串行实施；每项行为先 RED、再最小 GREEN。共享状态文件存在写冲突，不使用并行写入。

**目标：** 飞书收到 Codex 或 Claude 任务后立即创建任务卡，执行中只显示最新状态，终态把完整回答写入同一卡片，并发送一条简短通知。

**架构：** `messaging.progressSession` 统一管理卡片生命周期。平台能力声明终态通知；回复交付先处理附件并生成安全正文，再完成卡片，卡片失败时发送完整普通文本。

**技术栈：** Go、飞书 CardKit、`platform.Replier` / `platform.Stream`、Go testing。

---

## 文件职责

- `platform/platform.go`、`platform/platformtest/fake.go`：终态通知能力和可失败测试流。
- `feishu/replier.go`：按 CardKit 可用性声明 Streaming。
- `messaging/progress.go`、`messaging/progress_render.go`：即时开卡、终态和通知。
- `messaging/reply_delivery.go`：媒体投影、卡片终态和文本回退的唯一入口。
- Agent 执行入口：调用统一交付入口，不再各自 finish 后发送。

## Task 1：平台能力与测试流

**文件：** `platform/platform.go`、`platform/platformtest/fake.go`、`feishu/replier.go`、`feishu/replier_test.go`

- [x] **Step 1：写 RED 测试**

```go
func TestReplierCapabilitiesRequireCardKitForStreaming(t *testing.T) {
	without := NewReplier(nil, "ou_user").Capabilities()
	if without.Streaming || without.StreamCompletionNotification { t.Fatalf("without=%#v", without) }
	with := NewReplier(nil, "ou_user", &fakeCardKitClient{}).Capabilities()
	if !with.Streaming || !with.StreamCompletionNotification || with.FinalReplyOutsideStream {
		t.Fatalf("with=%#v", with)
	}
}
```

运行：`go test ./feishu -run TestReplierCapabilitiesRequireCardKitForStreaming -count=1 -timeout 60s`

预期：`StreamCompletionNotification` 不存在，编译失败。

- [x] **Step 2：实现能力和故障注入**

```go
// platform.Capabilities 新增：
StreamCompletionNotification bool

// platformtest.Replier 新增：
OpenStreamErr error

// platformtest.Stream 新增：
CompleteErr error
FailErr     error
```

`OpenStream`、`Complete`、`Fail` 优先返回对应注入错误；无错误时保持原记录行为。飞书能力实现为：

```go
func (r *Replier) Capabilities() platform.Capabilities {
	streaming := r.cardKit != nil
	return platform.Capabilities{
		Text: true, Typing: true, Image: true, File: true, Card: true,
		Streaming: streaming, Buttons: true,
		StreamCompletionNotification: streaming,
	}
}
```

- [x] **Step 3：GREEN 与提交**

```bash
go test ./platform/platformtest ./feishu -count=1 -timeout 60s
git add platform/platform.go platform/platformtest/fake.go feishu/replier.go feishu/replier_test.go
git commit -m "声明飞书任务卡终态通知能力"
```

## Task 2：任务开始时立即开卡

**文件：** `messaging/progress.go`、`messaging/progress_render.go`、`messaging/progress_test.go`

- [x] **Step 1：写 RED 测试**

```go
func TestNativeStreamOpensBeforeFirstAgentProgress(t *testing.T) {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	cfg := config.DefaultProgressConfig(); cfg.Mode = progressModeStream
	_, finish := NewHandler(nil, nil).startProgressSessionWithFinal(context.Background(), reply, "", "短任务", cfg)
	if reply.Stream.Options.Title != "短任务" || reply.Stream.Options.InitialContent != "正在处理任务，请稍候。" {
		t.Fatalf("options=%#v", reply.Stream.Options)
	}
	if !finish("最终结果", false) || reply.Stream.Completed != "最终结果" { t.Fatalf("stream=%#v", reply.Stream) }
}

func TestNativeStreamCreationFailureIsExplicitAndNotRetried(t *testing.T) {
	reply := platformtest.NewReplier(platform.Capabilities{Text: true, Streaming: true})
	reply.OpenStreamErr = errors.New("card unavailable")
	cfg := config.DefaultProgressConfig(); cfg.Mode = progressModeStream
	onProgress, finish := NewHandler(nil, nil).startProgressSessionWithFinal(context.Background(), reply, "", "任务", cfg)
	onProgress("进展：正在分析。")
	if finish("最终结果", false) { t.Fatal("failed stream consumed final") }
	want := "任务已开始，卡片创建失败，将以普通消息返回结果。"
	if len(reply.Texts) != 1 || reply.Texts[0] != want { t.Fatalf("texts=%#v", reply.Texts) }
}
```

运行：`go test ./messaging -run 'TestNativeStream(OpensBeforeFirstAgentProgress|CreationFailureIsExplicitAndNotRetried)' -count=1 -timeout 60s`

预期：尚未即时开卡，两个测试失败。

- [x] **Step 2：最小实现**

```go
func renderInitialCardProgress() string { return "正在处理任务，请稍候。" }
func renderCardCreationFallback() string { return "任务已开始，卡片创建失败，将以普通消息返回结果。" }
```

`progressSession` 增加 `streamOpenAttempted bool`。`start` 在 `progressModeAllowsProgress && Streaming` 时立即调用 `ensureStream`；失败只发送一次降级提示。`ensureStream` 在 `streamMu` 内只尝试一次，并传入：

```go
platform.StreamOptions{
	Title: progressTaskTitle(s.taskText, 60),
	InitialContent: renderInitialCardProgress(),
}
```

- [x] **Step 3：GREEN、负向边界与提交**

新增 `TestProgressOffModeDoesNotOpenNativeStream`，断言 `Mode=off` 时 `Stream.Options` 为空；保留默认 typing 和微信 stream 测试，证明本轮只改变飞书原生 stream。

```bash
go test ./messaging -run 'TestNativeStream|TestStartProgressSessionDefaultTypingModeDoesNotSendTextFeedback|TestStartProgressSessionStreamModeSendsLastStatusLine' -count=1 -timeout 60s
git add messaging/progress.go messaging/progress_render.go messaging/progress_test.go
git commit -m "让飞书任务卡在任务开始时立即显示"
```

## Task 3：终态通知

**文件：** `messaging/progress.go`、`messaging/progress_render.go`、`messaging/progress_test.go`、`messaging/handler_progress_task_test.go`

- [x] **Step 1：写表驱动 RED 测试**

```go
tests := []struct{ name string; cancel, failed bool; want string }{
	{name: "success", want: "任务已完成，请查看上方卡片。"},
	{name: "failure", failed: true, want: "任务执行失败，请查看上方卡片。"},
	{name: "stopped", cancel: true, want: "任务已停止，请查看上方卡片。"},
}
```

每项创建带 `StreamCompletionNotification` 的测试 Replier，必要时取消父 context，调用 `finish("终态正文", failed)`，断言只有对应通知。另加 `CompleteErr` 测试，断言 `finish` 返回 false 且不通知。

运行：`go test ./messaging -run TestNativeStreamTerminal -count=1 -timeout 60s`

预期：通知缺失。

- [x] **Step 2：实现终态选择**

```go
func renderStreamTerminalNotification(parentCanceled bool, failed bool, finalText string) string {
	if strings.TrimSpace(finalText) == "" || finalText == progressStatusOnlyComplete { return "" }
	if parentCanceled { return "任务已停止，请查看上方卡片。" }
	if failed { return "任务执行失败，请查看上方卡片。" }
	return "任务已完成，请查看上方卡片。"
}
```

`finishStream` 仅在 `Complete` / `Fail` 成功后，根据能力调用 `SendText`；通知失败记录日志但保持卡片为权威终态。保留 `FinalReplyOutsideStream` 兼容测试；新增默认飞书断言：完整结果进入卡片，文本只有简短通知。

- [x] **Step 3：GREEN 与提交**

```bash
go test ./messaging -run 'TestNativeStreamTerminal|TestSendToNamedAgentNativeStream' -count=1 -timeout 60s
git add messaging/progress.go messaging/progress_render.go messaging/progress_test.go messaging/handler_progress_task_test.go
git commit -m "完成飞书任务卡后发送简短通知"
```

## Task 4：媒体感知的统一交付

**文件：** `messaging/reply_delivery.go`、`messaging/agent_execution.go`、`messaging/codex_agent_task.go`、`messaging/codex_external_task.go`、`messaging/agent_broadcast.go` 及对应测试。

- [x] **Step 1：写 RED 测试**

附件测试创建允许目录内的 `report.pdf`；调用新入口后断言：文件独立发送、卡片正文包含 `已发送附件：report.pdf`、不包含绝对路径。终态失败测试注入 `CompleteErr`，断言只收到完整普通文本，没有完成通知。

```go
type replyDeliveryRequest struct {
	ctx         context.Context
	replyWriter platform.Replier
	userID      string
	agentName   string
	reply       string
}

type progressReplyDelivery struct {
	delivery replyDeliveryRequest
	failed      bool
	finish      func(string, bool) bool
}
```

运行：`go test ./messaging -run 'TestArtifactSendBackProjectsAttachmentResultIntoStream|TestTerminalCardFailureFallsBackToFullText' -count=1 -timeout 60s`

预期：统一入口尚不存在，编译失败。

- [x] **Step 2：实现投影和统一入口**

```go
type replyDeliveryProjection struct {
	text      string
	imageURLs []string
}

func (h *Handler) finishAndSendProgressReply(req progressReplyDelivery) bool {
	projection := h.prepareReplyDelivery(req.delivery)
	consumed := finishProgressWithReplyForPlatform(req.delivery.replyWriter, req.finish, projection.text, req.failed)
	h.sendReplyProjection(req.delivery, projection, consumed)
	return consumed
}
```

`prepareReplyDelivery(delivery)` 复用现有 allowed roots 校验、图片/文件分流和 `rewriteReplyWithAttachmentResults`。`sendReplyProjection` 仅在 `consumed=false` 时发正文，但始终保留远程图片 URL 行为。现有 `sendReplyWithMediaAfterStreamCore` 也复用这两个 helper，不复制附件逻辑。

- [x] **Step 3：迁移全部终态入口**

`agent_execution.go`、`codex_agent_task.go`、`codex_external_task.go` 改为构造 `progressReplyDelivery`。广播 worker 必须在自身 context 被 defer cancel 前，通过现有 serialized Replier 调用统一入口；随后发送 `skip=true` 的 result 只用于完成顺序同步。禁止把 `finish` 延迟到接收端，避免成功任务因 worker context 已取消而被误判为停止。未启动 progress session 的错误仍走普通回复。

补充回归：Claude blocking Agent 显式设置 `AgentInfo{Name: "claude", Type: "acp"}`，在返回前已 `StreamOpened`；Codex 后台任务和外部 watcher 终态写回原卡；飞书广播维持 serialized Replier 无重入。再增加 `OpenStreamErr` 集成断言，证明开卡失败提示后最终完整结果仍以普通文本送达。

- [x] **Step 4：GREEN 与提交**

```bash
go test ./messaging ./feishu ./platform/platformtest -count=1 -timeout 60s
git add messaging/reply_delivery.go messaging/agent_execution.go messaging/codex_agent_task.go messaging/codex_external_task.go messaging/agent_broadcast.go messaging/artifact_sendback_test.go messaging/handler_progress_task_test.go messaging/handler_codex_live_message_control_test.go messaging/handler_codex_live_recovery_test.go
git commit -m "统一飞书任务卡最终结果交付"
```

## Task 5：全仓验收

- [x] `gofmt` 所有改动 Go 文件。
- [x] 运行 `go test ./... -count=1 -timeout 120s`。
- [x] 运行 `go test -race ./... -count=1 -timeout 120s`。
- [x] 运行 `go vet ./...`、`staticcheck ./...`。
- [x] 运行 `python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。
- [x] Review gate 核对即时模式边界、单次开卡、卡片成功才通知、失败完整回退、附件根目录校验、Codex/Claude/watcher/broadcast 共用实现。
- [x] 更新 `tasks/todo.md` 的 P5.6c/P5.6d 和 `tasks/lessons.md`，提交：`完成飞书即时任务卡片验收`。
