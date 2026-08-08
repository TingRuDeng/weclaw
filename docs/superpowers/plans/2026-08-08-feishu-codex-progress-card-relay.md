# 飞书 Codex 进度卡接力 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让每条被活动 Codex turn 成功接受的飞书引导消息立即生成一张新的权威进度卡，并让飞书原生任务卡支持可手动折叠、终态自动折叠和跨重启幂等收敛旧卡。

**Architecture:** 消息层继续以 `progressSession.streamMu` 作为权威 stream 切换和终态竞争的唯一互斥边界，通过可选的结构化展示接口把摘要与完整时间线交给平台。飞书 adapter 使用稳定 CardKit 组件更新普通进度，使用可持久化 checkpoint 执行终态和旧卡 supersede；terminal outbox 在同一 active-stream reservation 内原子保存最新权威引用和待收敛旧卡操作。两个 Codex steer 入口在同一个 thread 控制锁内完成“接受引导 -> 迁卡”，不创建第二个 task、turn、writer 或私有引导队列。

**Tech Stack:** Go 1.26.5、飞书 Card JSON 2.0 / CardKit、现有 `platform` 可选能力接口、现有 JSON terminal outbox、Go `testing`。

## Global Constraints

- 以 `docs/superpowers/specs/2026-08-08-feishu-codex-progress-card-relay-design.md` 为已确认需求；不改变 shared app-server、writer lease、CLI/App/飞书平等入口语义。
- 每条成功 steer 的引导立即产生独立接力卡，不做 3 秒合并、延迟或批处理。
- `turn/steer` 成功后才允许迁卡；迁卡失败不得重发引导。
- 新卡 durable reference 持久化并成为权威前，旧卡继续接收进度；权威切换后不得因旧卡收敛失败而回滚。
- 终态和迁卡都必须持有 `progressSession.streamMu`；先获得该锁并 claim 状态的一方决定最终写入位置。
- 折叠不替代现有 Card JSON 字节上限、时间线限制或超长续卡机制。
- 非飞书及不支持新可选接口的平台保持现有 `Stream.Update` 与文字确认行为。
- outbox 新字段保持 JSON 向后兼容，`terminalOutboxVersion` 继续为 `1`；不得删除或重写已有状态文件。
- 最终回答继续通过独立结果卡发送，不写回进度面板。
- 不新增配置项，不新增核心依赖，不执行发布、推送、本机更新或重启。
- 保留用户已有的 `tasks/lessons.md` 修改与 `.codex/` 未跟踪目录；每次提交只暂存本任务列出的文件。

---

## Task 1: 定义结构化进度展示契约并保留字符串兼容路径

**Files:**

- Modify: `platform/reply.go`
- Modify: `messaging/progress.go`
- Modify: `messaging/task_state.go`
- Modify: `messaging/task_progress_snapshot_test.go`
- Modify: `messaging/progress_reanchor_test.go`

- [ ] **Step 1: 先写失败测试，锁定摘要不从 Markdown 反解析**

在 `messaging/task_progress_snapshot_test.go` 增加 `TestProgressReanchorSnapshotCarriesStructuredSummaryAndDetails`。构造包含 commentary、tool 和本地引导记录的 `activeAgentTask`，断言：

```go
progress, snapshot, ok := task.progressReanchorSnapshot()
if !ok || progress == nil {
	t.Fatal("expected reanchorable progress session")
}
if snapshot.summary != "已接收新的补充输入。" {
	t.Fatalf("summary=%q", snapshot.summary)
}
if !snapshot.structured || len(snapshot.timelineItems) == 0 {
	t.Fatalf("snapshot=%#v", snapshot)
}
if !strings.Contains(snapshot.text, "已接收新的补充输入。") {
	t.Fatalf("details=%q", snapshot.text)
}
```

在 `messaging/progress_reanchor_test.go` 增加一个实现结构化接口的 test stream，断言 `sendSnapshotContent` 和 `reanchor` 传出的 `Summary` 是 reducer 的 `latest`，`Details` 才包含完整时间线；测试中不得从 `Details` 截取第一行来构造摘要。

- [ ] **Step 2: 运行目标测试并确认按预期失败**

Run:

```bash
go test ./messaging -run 'TestProgress(ReanchorSnapshotCarriesStructuredSummaryAndDetails|SessionUsesStructuredPresentation)' -count=1
```

Expected: 编译失败，指出 `StreamPresentation`、`UpdatePresentation` 或 `progressCardSnapshot.summary` 尚不存在。

- [ ] **Step 3: 在平台层增加完全可选的结构化接口**

在 `platform/reply.go` 增加以下类型；现有 `Stream` 不增加方法，避免破坏微信和测试 adapter：

```go
type StreamPresentation struct {
	Summary string
	Details string
}

type StructuredProgressStream interface {
	UpdatePresentation(ctx context.Context, presentation StreamPresentation) error
}

type StreamPresentationPreflighter interface {
	PreflightPresentation(presentation StreamPresentation) error
}

type StreamOptions struct {
	Title               string
	InitialContent      string
	InitialPresentation *StreamPresentation
}
```

`InitialContent` 保持为所有平台的兼容值；支持结构化展示的平台只在 `InitialPresentation != nil` 时启用摘要 + 折叠面板结构。

- [ ] **Step 4: 让 reducer 快照显式携带摘要**

在 `progressCardSnapshot` 增加 `summary string`。`onTaskProgress` 使用 `taskProgressUpdate.latest` 填充 `summary`，使用现有 `card` 填充 `text`。把 `activeAgentTask.progressReanchorSnapshot` 的返回值改为：

```go
func (t *activeAgentTask) progressReanchorSnapshot() (*progressSession, progressCardSnapshot, bool)
```

该方法在持有 `t.mu` 时直接从 `taskViewState` 生成 `summary`、`text`、`structured`、`currentExplanation` 和 `timelineItems`，使刚记录的本地引导状态可直接初始化新卡，不经过旧卡异步更新队列。

- [ ] **Step 5: 统一构造展示值并接入可选接口**

在 `progressSession` 增加只在 `streamMu` 已持有时调用的 helper：

```go
func (s *progressSession) snapshotPresentationLocked(snapshot progressCardSnapshot) platform.StreamPresentation
```

规则固定为：

- `Summary` 使用 `snapshot.summary`；为空时回退到 `snapshot.text`。
- `Details` 使用 `activeSnapshotContentLocked(snapshot)`，保留 prefix、完整时间线、当前 explanation 和底部思考中标记。
- `sendSnapshotContent` 优先调用 `StreamPresentationPreflighter` 与 `StructuredProgressStream.UpdatePresentation`，否则继续调用现有 `PreflightUpdate` 与 `Stream.Update`。
- `ensureStreamLocked`、`reanchor` 和 `continueProgressStreamLocked` 同时传入 `InitialContent` 与 `InitialPresentation`；不支持结构化能力的平台只消费 `InitialContent`。
- 组件预检返回 `platform.ErrStreamContentTooLarge` 时继续进入既有续卡路径，不能因折叠面板跳过容量检查。

- [ ] **Step 6: 运行测试并提交平台契约**

Run:

```bash
go test ./platform ./messaging -run 'TestProgress(ReanchorSnapshotCarriesStructuredSummaryAndDetails|SessionUsesStructuredPresentation|SessionReanchor)' -count=1
```

Expected: PASS。

Commit:

```bash
git add platform/reply.go messaging/progress.go messaging/task_state.go messaging/task_progress_snapshot_test.go messaging/progress_reanchor_test.go
git commit -m "feat: 增加结构化进度展示契约"
```

---

## Task 2: 构建飞书可折叠任务卡并改为稳定组件更新

**Files:**

- Modify: `feishu/card.go`
- Modify: `feishu/task_card.go`
- Modify: `feishu/stream.go`
- Modify: `feishu/card_test.go`
- Modify: `feishu/stream_test.go`
- Modify: `feishu/adapter_approval_card_update_test.go`

- [ ] **Step 1: 写 Card JSON 结构与状态矩阵测试**

在 `feishu/card_test.go` 增加表驱动测试 `TestBuildTaskCardUsesCollapsibleProgressPanel`，解码 JSON 后逐项断言：

```go
const (
	cardProgressSummaryID = "progress_summary"
	cardProgressPanelID   = "progress_panel"
)
```

- 顶层 `progress_summary` Markdown 位于 `collapsible_panel` 之前。
- `collapsible_panel.element_id == "progress_panel"`。
- panel header 标题为“完整进度”。
- panel 内唯一 Markdown 的 `element_id == "main_content"`。
- `thinking`、`streaming` 的新任务卡 `expanded == true`。
- `superseded`、`done`、`stopped`、`error` 卡 `expanded == false`。
- 普通结果卡未设置 `Collapsible` 时仍使用原有单个 `main_content`，不出现 panel。

- [ ] **Step 2: 写组件更新测试，禁止普通进度全量重建卡片**

扩展 `fakeCardKitClient`，记录每次 `StreamContent` 的 `cardID`、`elementID`、正文和 sequence。新增 `TestTaskCardStreamUpdatesSummaryAndDetailsWithoutReplacingCard`：

```go
err := stream.(platform.StructuredProgressStream).UpdatePresentation(ctx, platform.StreamPresentation{
	Summary: "正在运行测试",
	Details: "读取代码\n\n运行测试\n\n思考中.....",
})
```

断言 `UpdateCard` 调用数为 `0`，`StreamContent` 分别命中 `progress_summary` 和 `main_content`，sequence 严格递增；随后再次更新，仍不出现全卡更新。

在 `feishu/adapter_approval_card_update_test.go` 增加断言：审批导致的全卡重建仍包含当前 summary、details、panel 和 approvals，且活动状态的默认 `expanded` 仍为 `true`。

- [ ] **Step 3: 运行目标测试并确认失败**

Run:

```bash
go test ./feishu -run 'Test(BuildTaskCardUsesCollapsibleProgressPanel|TaskCardStreamUpdatesSummaryAndDetailsWithoutReplacingCard|.*Approval.*ProgressPanel)' -count=1
```

Expected: 失败，当前卡片没有 `collapsible_panel`，tracked task 的更新仍调用 `UpdateCard`。

- [ ] **Step 4: 扩展卡片和 registry 快照**

在 `cardOptions` 与 `taskCardState` 增加并完整克隆以下字段：

```go
type cardOptions struct {
	Status             string
	Title              string
	Summary            string
	Content            string
	Approvals          []string
	Collapsible        bool
	Expanded           bool
	InlineActiveStatus bool
}
```

`buildCardV2` 的结构规则为：

1. 非折叠卡保持现有结构。
2. 折叠任务卡把状态元素和 `progress_summary` 放在 panel 外。
3. `progress_panel` 的 `header.title` 使用 plain text “完整进度”。
4. `main_content` 放入 panel 的 `elements`。
5. 结构终态由调用方显式传入 `Expanded: false`；活动新卡显式传入 `Expanded: true`。

`taskCardRegistry` 增加 `updatePresentationWithSequences`，在一次 registry 锁内更新 summary/details，并分配两个严格递增的 sequence。snapshot、审批全卡重建和 durable reference 导出必须读取同一份 summary/details/expanded 状态。

- [ ] **Step 5: 实现结构化组件更新**

`openCardKitStreamWithMode` 在 `trackTask && opts.InitialPresentation != nil` 时创建折叠任务卡，并在 stream 中保存 `lastSummary`、`lastContent` 和 `collapsible=true`。

在 `feishuStream` 实现：

```go
func (s *feishuStream) PreflightPresentation(p platform.StreamPresentation) error
func (s *feishuStream) UpdatePresentation(ctx context.Context, p platform.StreamPresentation) error
```

实现约束：

- 预检必须用完整 `buildCardV2` 结果比较 `feishuCardJSONSoftLimitBytes`。
- 普通结构化更新复用现有 throttle，只保留最后一个完整 `StreamPresentation`，不能分别丢弃 summary 或 details。
- 实际写入在 `ioMu` 内依次更新 `progress_summary` 和 `main_content`，每次使用 registry 分配的独立 sequence。
- `progress_summary` 只显示活动状态与最新摘要，不包含完整时间线；`main_content` 保存完整时间线和思考中标记。
- 任一组件写入失败时保留内存/registry 的最新完整快照；下一次进度更新再次写入两个组件。
- 对非折叠 stream，`Update`、throttle、重试和终态行为保持现状。
- 对折叠 tracked task，普通 `Update` 也只能更新稳定 Markdown 组件，不能再调用 `UpdateCard`。

- [ ] **Step 6: 让结构终态自动折叠**

`prepareTerminalUpdate` 与现有 `prepareSupersedeUpdate` 从 registry 同一快照重建整卡，并设置 `Expanded: false`。终态只执行一次停止 streaming + 全卡结构更新；普通流式更新不得写 `expanded`，从而保留用户手动展开状态。

- [ ] **Step 7: 运行飞书测试并提交**

Run:

```bash
go test ./feishu -count=1 -timeout 120s
```

Expected: PASS，且原有结果卡、审批卡、停止卡和 standalone stream 测试不变。

Commit:

```bash
git add feishu/card.go feishu/task_card.go feishu/stream.go feishu/card_test.go feishu/stream_test.go feishu/adapter_approval_card_update_test.go
git commit -m "feat: 支持飞书进度卡折叠与组件更新"
```

---

## Task 3: 为旧卡 supersede 增加可持久化幂等 checkpoint

**Files:**

- Modify: `platform/reply.go`
- Modify: `feishu/stream.go`
- Modify: `feishu/stream_test.go`
- Modify: `feishu/replier_test.go`

- [ ] **Step 1: 写 checkpoint 生成、重复投递和旧引用兼容测试**

新增以下测试：

- `TestFeishuPrepareSupersedeFromReferencePreservesProgressAndCollapsesPanel`
- `TestFeishuDeliverSupersedeCheckpointIsIdempotent`
- `TestFeishuPrepareSupersedeFromLegacyReferenceUsesContentFallback`

重复投递测试使用 `fakeIdempotentCardKitClient` 对同一 checkpoint 调用两次，断言底层 disable/update 各只产生一次有效写入，两个调用使用相同 operation ID；卡片 JSON 状态为 `superseded`、`expanded=false`，并保留旧卡 summary、details 和 approvals。

- [ ] **Step 2: 运行测试并确认失败**

Run:

```bash
go test ./feishu -run 'TestFeishu(PrepareSupersede|DeliverSupersede)' -count=1
```

Expected: 编译失败，因为平台尚无 supersede checkpoint 契约。

- [ ] **Step 3: 增加平台可选能力接口**

在 `platform/reply.go` 增加：

```go
type SupersedeCheckpoint struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type DurableStreamSupersedePreparer interface {
	PrepareSupersedeFromReference(reference DurableStreamReference, notice string, operationID string) (SupersedeCheckpoint, error)
}

type PreparedSupersedableStream interface {
	DeliverPreparedSupersede(ctx context.Context, checkpoint SupersedeCheckpoint) error
}

type DurableSupersedeReplier interface {
	DeliverSupersede(ctx context.Context, checkpoint SupersedeCheckpoint) error
}
```

这些接口只用于支持持久化迁移的平台；现有 `SupersedableStream.Supersede` 保留为无 outbox 或旧 adapter 的 best-effort 兼容路径。

- [ ] **Step 4: 扩展飞书 durable reference，保持 v1 解码兼容**

在 `feishuStreamReferencePayload` 增加可选 `summary`、`details`、`collapsible` 字段，同时保留现有 `content`：

```go
type feishuStreamReferencePayload struct {
	CardID     string   `json:"card_id"`
	Title      string   `json:"title"`
	Sequence   int      `json:"sequence"`
	Content    string   `json:"content,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Details    string   `json:"details,omitempty"`
	Collapsible bool    `json:"collapsible,omitempty"`
	Approvals  []string `json:"approvals,omitempty"`
}
```

旧 payload 没有新字段时，`Content` 同时作为 summary/details 回退；kind 继续使用 `feishu.cardkit.stream.v1`，不破坏已落盘引用。

- [ ] **Step 5: 实现幂等 supersede checkpoint**

新增 kind `feishu.cardkit.supersede.v1`。`PrepareSupersedeFromReference` 校验旧引用，使用旧 sequence 的后两个值生成 disable/update 操作，并通过稳定迁移 ID派生两个 UUID：

```go
disableID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":disable")).String()
updateID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":update")).String()
```

checkpoint 的 payload 复用 `feishuStreamTerminalOp` 的 card ID、sequence、operation ID 和完整卡 JSON。`DeliverSupersede` 只接受 supersede kind，并要求 `idempotentCardKitClient`；`DeliverPreparedSupersede` 先在内存中关闭旧 stream、取消 pending timer 和 recovery callback，再投递持久化 checkpoint。supersede 不调用 `DestroyCard`，历史卡仍需可见。

- [ ] **Step 6: 运行测试并提交**

Run:

```bash
go test ./platform ./feishu -run 'TestFeishu(PrepareSupersede|DeliverSupersede|.*DurableReference)' -count=1
```

Expected: PASS。

Commit:

```bash
git add platform/reply.go feishu/stream.go feishu/stream_test.go feishu/replier_test.go
git commit -m "feat: 持久化飞书旧进度卡收敛操作"
```

---

## Task 4: 扩展 terminal outbox 以原子保存并重试旧卡收敛

**Files:**

- Modify: `messaging/terminal_outbox.go`
- Modify: `messaging/terminal_outbox_test.go`
- Modify: `messaging/reply_delivery.go`
- Modify: `messaging/platform_optional_reply_capabilities_test.go`

- [ ] **Step 1: 写持久化、活动 reservation 重试和独立失败预算测试**

在 `messaging/terminal_outbox_test.go` 增加：

- `TestTerminalOutboxReanchorPersistsNewAuthorityAndPendingSupersedeAtomically`
- `TestTerminalOutboxRetriesSupersedeWhileReservationIsPreparing`
- `TestTerminalOutboxRestartReplaysPendingSupersedeBeforeTerminalRecovery`
- `TestTerminalOutboxSupersedeFailureDoesNotConsumeTerminalAttempts`
- `TestTerminalOutboxLoadsVersionOneEntryWithoutPendingSupersedes`
- `TestTerminalOutboxKeepsDeliveredTerminalWhileSupersedeNeedsRedrive`

持久化失败测试先保存文件字节，再注入写入错误，断言内存和磁盘都仍指向旧 authority，且 pending list 没有半条记录。

- [ ] **Step 2: 运行测试并确认失败**

Run:

```bash
go test ./messaging -run 'TestTerminalOutbox(Reanchor|RetriesSupersede|RestartReplaysPendingSupersede|SupersedeFailure|LoadsVersionOne|KeepsDeliveredTerminal)' -count=1
```

Expected: 编译失败，`pendingStreamSupersede` 与 `reanchorStreamReservation` 尚不存在。

- [ ] **Step 3: 增加向后兼容的数据结构**

定义：

```go
type pendingStreamSupersede struct {
	ID           string                       `json:"id"`
	Route        platform.DeliveryRoute       `json:"route"`
	Checkpoint   platform.SupersedeCheckpoint `json:"checkpoint"`
	Attempts     int                          `json:"attempts,omitempty"`
	NextAttempt  time.Time                    `json:"next_attempt"`
	LastError    string                       `json:"last_error,omitempty"`
	DeadLetter   bool                         `json:"dead_letter,omitempty"`
	DeadLetterAt time.Time                    `json:"dead_letter_at,omitempty"`
}
```

在 `terminalOutboxEntry` 增加：

```go
PendingSupersedes []pendingStreamSupersede `json:"pending_supersedes,omitempty"`
```

clone、validate、load、persist、status 和 redrive 都要覆盖新字段。`ID` 使用迁移 UUID；`Route` 固定为旧卡创建时的平台/account/chat 路由；checkpoint 不允许空 kind、空 payload 或非法 JSON。旧 v1 文件没有该字段时按空切片加载。

- [ ] **Step 4: 实现单次 fsync 的权威切换事务**

增加：

```go
func (o *terminalOutbox) reanchorStreamReservation(
	id string,
	newRoute platform.DeliveryRoute,
	newReference platform.DurableStreamReference,
	pending pendingStreamSupersede,
) error
```

该方法在同一个 `o.mu` 临界区内：clone 旧 entry、替换 `Route`/`Stream`、追加去重后的 pending supersede、刷新时间、校验并调用一次 `persistLocked`。任何失败恢复整个旧 entry；相同 `pending.ID` 重试时只能保留一条。

`refreshStreamReservation`、`stageReservation` 和 `commitReservation` 必须保留已有 `PendingSupersedes`，不得在普通进度刷新或终态准备时清空。

同时增加以下内部操作，所有操作都必须在持久化失败时恢复修改前快照：

```go
func (o *terminalOutbox) completePendingStreamSupersede(entryID string, pendingID string) error
func (o *terminalOutbox) recordPendingStreamSupersedeFailure(entryID string, pendingID string, deliveryErr error) error
func (o *terminalOutbox) attemptPendingStreamSupersede(ctx context.Context, entryID string, pendingID string) error
```

- [ ] **Step 5: 增加独立的 pending supersede 调度器**

在 outbox run loop 中先收集到期的 pending supersede，再处理普通 terminal entry：

- pending supersede 即使父 entry 位于内存 `preparing` 集合也可投递。
- 同一父 entry 的 supersede 与 terminal attempt 不能并发修改状态。
- 使用 pending 自身的 `Attempts`、`NextAttempt`、`LastError` 和 `DeadLetter`，不能修改父 entry 的 terminal `Attempts`/`DeadLetter`。
- 通过 pending 自己的 `Route` 重建 replier，并调用 `platform.DurableSupersedeReplier.DeliverSupersede`。
- `reply_delivery.go` 增加 `optionalDurableStreamSupersedePreparer`、`optionalDurableSupersedeReplier` 和持有 `serializedReplier.mu` 的 wrapper；对应 capability 测试证明序列化包装不会丢失准备或投递接口。
- 成功后只删除对应 pending；活动 reservation 继续保留最新 `Stream`。
- 父 terminal 已全部投递但仍有 pending 时保留 entry；pending 清空后再删除 entry。
- pending 进入 dead letter 后保留父 entry，`redrive` 可单独重置 pending 的失败状态；已经成功的 terminal 阶段不得重复发送。
- 记录 `task.card_supersede_pending`、`task.card_superseded`、`task.card_supersede_retry` 和 `task.card_supersede_dead_letter`，不得记录引导正文。

- [ ] **Step 6: 运行 outbox 全量测试并提交**

Run:

```bash
go test ./messaging -run 'TestTerminalOutbox' -count=1 -timeout 120s
```

Expected: PASS，包括已有 crash recovery、result delivery、dead-letter 和 redrive 测试。

Commit:

```bash
git add messaging/terminal_outbox.go messaging/terminal_outbox_test.go messaging/reply_delivery.go messaging/platform_optional_reply_capabilities_test.go
git commit -m "feat: 重试进度卡接力收敛操作"
```

---

## Task 5: 把 reanchor 与超长续卡收敛成同一原子事务

**Files:**

- Modify: `messaging/progress.go`
- Modify: `messaging/progress_reanchor_test.go`
- Modify: `messaging/progress_rollover_test.go`
- Modify: `messaging/terminal_outbox_test.go`
- Modify: `messaging/codex_session_acquire.go`

- [ ] **Step 1: 写权威切换顺序、失败边界与竞争测试**

新增或扩展测试覆盖：

- `TestProgressSessionReanchorPersistsBeforeAuthoritySwitch`
- `TestProgressSessionReanchorPersistenceFailureKeepsOldAuthority`
- `TestProgressSessionReanchorSupersedeFailureKeepsNewAuthorityAndQueuesRetry`
- `TestProgressSessionReanchorWinsConcurrentTerminal`
- 现有 `TestProgressSessionTerminalWinsConcurrentReanchor`
- `TestProgressRolloverUsesDurableReanchorTransaction`

测试 stream 暴露事件序列，成功路径必须严格为：

```text
open-new
export-new-reference
persist-new-reference-and-old-supersede
bind-new-card
swap-authority
deliver-old-supersede
```

持久化失败时断言后续 `Update` 和终态仍只进入旧 stream；旧卡 supersede 失败时断言后续 `Update` 和终态只进入新 stream，outbox 留下一条可重试操作。

- [ ] **Step 2: 运行测试并确认失败**

Run:

```bash
go test ./messaging -run 'TestProgress(SessionReanchor|Rollover)' -count=1
```

Expected: 新顺序与故障注入测试失败，当前实现先刷新新引用再 best-effort supersede，无法跨重启补偿。

- [ ] **Step 3: 定义内部迁移输入与结果**

在 `messaging/progress.go` 增加包内类型：

```go
type progressReanchorRequest struct {
	Context      context.Context
	Reply        platform.Replier
	Snapshot     progressCardSnapshot
	Title        string
	TransitionID string
	Notice       string
}

type progressReanchorResult struct {
	Moved             bool
	SupersedePending  bool
}
```

`TransitionID` 由调用方提供稳定 UUID；超长续卡在当前 reservation 内生成并持久化一个新 UUID。对重复的同一迁移动作，outbox 以 transition ID 去重。

- [ ] **Step 4: 实现持锁迁移事务**

新增 `reanchorStreamLocked(request)`，只允许在 `streamMu` 已持有时调用。执行顺序固定为：

1. 检查 `finished`、`terminalClaimed`、旧 stream 与新 reply。
2. 从当前结构化 snapshot 生成 `InitialContent` 与 `InitialPresentation`。
3. 导出旧 stream durable reference；通过旧 reply 的 preparer 生成 supersede checkpoint，但此时不关闭旧 stream。
4. `OpenStream` 创建新卡并导出新 durable reference。
5. 若 recovery reservation、route、durable 能力齐全，调用 `reanchorStreamReservation` 一次持久化新 authority 与旧卡 pending 操作。
6. 只有第 5 步成功后才更新 task-card binding、`s.stream`、`s.reply`、`lastContent`、segment state 和 recovery callback。
7. 使用旧 stream 的 `DeliverPreparedSupersede` 投递；成功时从 outbox 清除 pending，失败时保留 pending 并返回 `Moved=true, SupersedePending=true`。

故障处理固定为：

- 新卡创建失败：返回未移动，旧 authority 不变。
- 新卡已创建但引用导出或 outbox 持久化失败：不切换 authority；尽力把新卡标为“任务卡迁移失败，后续进展仍保留在原卡”，但该清理失败不能覆盖原始错误。
- outbox/route/durable 能力不齐：保留原有内存原子切换和 `SupersedableStream.Supersede` best-effort 路径，保证非飞书平台兼容。
- 权威切换后旧卡投递失败：不回滚，不把 Codex 任务标为失败。

- [ ] **Step 5: 让两类迁卡复用事务**

把 `reanchor` 签名改为接收结构化 snapshot 和 transition ID；`reanchorActiveCodexTask` 使用 `progressReanchorSnapshot` 的结构化返回值。`continueProgressStreamLocked` 只负责选定新 segment anchor/title，然后调用同一迁移事务；现有超长卡提示保持“后续进度见第 N 张卡片”。

- [ ] **Step 6: 运行进度与 outbox 回归测试并提交**

Run:

```bash
go test ./messaging -run 'TestProgress|TestTerminalOutbox' -count=1 -timeout 180s
```

Expected: PASS。

Commit:

```bash
git add messaging/progress.go messaging/progress_reanchor_test.go messaging/progress_rollover_test.go messaging/terminal_outbox_test.go messaging/codex_session_acquire.go
git commit -m "refactor: 原子切换权威进度卡"
```

---

## Task 6: 统一两个 Codex 引导入口并为每条成功引导创建接力卡

**Files:**

- Create: `messaging/codex_guide_reanchor.go`
- Modify: `messaging/platform_message.go`
- Modify: `messaging/agent_execution.go`
- Modify: `messaging/codex_task_types.go`
- Modify: `messaging/codex_task_start.go`
- Modify: `messaging/task_commands.go`
- Modify: `messaging/task_external_control.go`
- Modify: `messaging/platform_commands.go`
- Modify: `messaging/pending_task_controls.go`
- Modify: `messaging/handler_codex_live_message_control_test.go`
- Modify: `messaging/handler_codex_rollout_task_test.go`
- Modify: `messaging/pending_task_controls_test.go`

- [ ] **Step 1: 写直接消息路径的成功、失败与快速连续引导测试**

在 `handler_codex_live_message_control_test.go` 增加：

- `TestLiveCodexGuideCreatesRelayCardWithoutSuccessText`
- `TestThreeRapidLiveCodexGuidesCreateThreeOrderedRelayCards`
- `TestLiveCodexGuideSteerFailureDoesNotCreateRelayCard`
- `TestLiveCodexGuideReanchorFailureWarnsWithoutRepeatingSteer`
- `TestLiveCodexGuideWithoutNativeProgressCardKeepsSuccessText`

三条快速引导测试逐条发送三个不同 message ID，断言三次 `SteerCodexThread`、三张新卡、每张上一权威卡恰好 supersede 一次、只有最后一张收到后续 progress/terminal，且没有“已发送到当前共享 Codex 任务。”文字消息。

- [ ] **Step 2: 写 `/guide` 与卡片按钮路径测试**

在 `handler_codex_rollout_task_test.go` 和 `pending_task_controls_test.go` 增加：

- `/guide` 成功后复用同一接力 helper，创建新卡且返回空成功文字。
- 同一 pending control revision 只能 steer 和建卡一次。
- steer 失败保留原错误文字且不建卡。
- steer 成功但迁卡失败只发送“引导已送达，但任务卡迁移失败”，不恢复 pending、不再次 steer。
- 两个并发 `/guide` 通过 thread 控制锁串行，卡片 authority 顺序与 steer 接受顺序一致。

- [ ] **Step 3: 运行目标测试并确认失败**

Run:

```bash
go test ./messaging -run 'Test(LiveCodexGuide|ThreeRapidLiveCodexGuides|.*Pending.*Guide|.*Rollout.*Guide)' -count=1 -timeout 120s
```

Expected: 失败，当前成功路径只发送文字，不创建接力卡；外部 `/guide` 的 runtime 检查锁没有覆盖 steer + reanchor。

- [ ] **Step 4: 贯穿稳定入站消息身份**

在 `platformMessageRuntime.agentRequest`、`agentMessageRequest` 和 `codexAgentTaskOptions` 增加 `messageKey`。正常平台消息使用 `platformMessageDedupKey(runtime.msg)`；命令和卡片按钮的 `taskCommandRequest` 也携带同一 key。MessageID 为空或为 `0` 时使用现有 `clientID` 与 task ID 组合生成本次进程内稳定 key，不把消息正文用于 ID。

迁移 UUID 使用：

```go
func codexGuideTransitionID(taskID string, messageKey string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("weclaw-guide\x00"+taskID+"\x00"+messageKey)).String()
}
```

该 ID 只用于迁移 trace、outbox 去重和 CardKit operation ID 派生。

- [ ] **Step 5: 增加 steer 成功后的统一 helper**

在新文件 `messaging/codex_guide_reanchor.go` 定义：

```go
type codexGuideDeliveryResult struct {
	Reanchored bool
	ReplyText  string
}

func (h *Handler) completeAcceptedCodexGuide(
	ctx context.Context,
	task *activeAgentTask,
	reply platform.Replier,
	messageKey string,
	localProgress string,
) codexGuideDeliveryResult
```

helper 的行为顺序：

1. 记录 `guide.accepted` trace，不记录引导正文。
2. 通过 reducer 记录 `localProgress`。
3. 从 `task.progressReanchorSnapshot()` 取得结构化最新快照。
4. 没有原生进度卡时返回现有成功文字。
5. 调用 `progress.reanchor`；成功时返回空 `ReplyText`，新卡即成功反馈。
6. steer 已成功但迁卡返回错误时返回 `fmt.Sprintf("引导已送达，但任务卡迁移失败: %s", sanitizeAgentError(err.Error()))`。
7. 终态已先 claim 或没有可迁移 stream 时返回现有成功文字，不伪造新卡。

- [ ] **Step 6: 让 thread 控制锁覆盖完整事务**

直接消息路径继续利用 `startCodexAgentTask` 已持有的 thread 控制锁，在 `SteerCodexThread` 成功后、函数返回前调用 helper。

外部 `/guide` 路径把 `resolveExternalCodexControl` 拆为“获取 cached target 的 wrapper”和“假定 thread 锁已持有的 runtime 校验 helper”。`steerPendingGuideToExternalCodex` 只获取一次 thread 控制锁，并在锁内完成：runtime 校验、pending reservation、`SteerCodexThread`、`finishExternalCodexGuide(delivered=true)`、记录本地进度和 reanchor。停止路径继续使用 wrapper，避免嵌套获取同一把锁。

成功创建接力卡时，`handleGuideCommand` 对空 `ReplyText` 不调用 `sendPlatformText`；错误和降级成功文字仍立即发送。

- [ ] **Step 7: 运行引导与会话回归测试并提交**

Run:

```bash
go test ./messaging -run 'Test(LiveCodexGuide|ThreeRapidLiveCodexGuides|.*Guide|.*Codex.*Control|.*Codex.*Reanchor)' -count=1 -timeout 180s
```

Expected: PASS，且每条成功引导对应一张新卡。

Commit:

```bash
git add messaging/codex_guide_reanchor.go messaging/platform_message.go messaging/agent_execution.go messaging/codex_task_types.go messaging/codex_task_start.go messaging/task_commands.go messaging/task_external_control.go messaging/platform_commands.go messaging/pending_task_controls.go messaging/handler_codex_live_message_control_test.go messaging/handler_codex_rollout_task_test.go messaging/pending_task_controls_test.go
git commit -m "feat: 为每条 Codex 引导接力进度卡"
```

---

## Task 7: 同步当前事实文档并执行最小充分全仓验证

**Files:**

- Modify: `README_CN.md`
- Modify: `README.md`
- Modify: `docs/AI_CONTEXT.md`
- Modify: `docs/superpowers/specs/2026-08-08-feishu-codex-progress-card-relay-design.md`
- Verify only: `tasks/lessons.md`
- Verify only: `.codex/`

- [ ] **Step 1: 更新用户说明和架构事实**

文档只写已经由测试证明的当前行为：

- 飞书每条成功的活动 turn 引导会创建独立接力卡。
- 旧卡显示已转移并折叠，最新活动卡默认展开且可手动折叠，结构终态自动折叠。
- 普通进度更新只写稳定组件；最终结果仍独立发送。
- outbox 会跨重启恢复最新权威卡并补偿旧卡 supersede。
- 非原生卡片平台继续使用原有文字反馈。

把设计文档状态改为“已实施，待发布验收”；在没有真实飞书端证据前不得写“已上线”或“移动端验收通过”。

- [ ] **Step 2: 运行受影响包测试**

Run:

```bash
go test ./feishu ./messaging ./platform -count=1 -timeout 180s
```

Expected: PASS。

- [ ] **Step 3: 运行全仓测试与静态检查**

Run each command separately:

```bash
go test ./... -count=1 -timeout 180s
go test -race ./... -count=1 -timeout 240s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

Expected: 全部退出码为 `0`；`go mod tidy -diff` 无输出；文档 validator 通过。

- [ ] **Step 4: 复核 diff、敏感信息和用户改动边界**

Run:

```bash
git status --short --branch
git diff --stat
git diff --check
git diff -- README_CN.md README.md docs/AI_CONTEXT.md docs/superpowers/specs/2026-08-08-feishu-codex-progress-card-relay-design.md
rg -n 'TODO|TBD|FIXME|api[_-]?key|access[_-]?token|secret' platform/reply.go messaging feishu README.md README_CN.md docs/AI_CONTEXT.md docs/superpowers/specs/2026-08-08-feishu-codex-progress-card-relay-design.md
```

确认 `tasks/lessons.md` 和 `.codex/` 没有进入本任务 diff 或暂存区；命中的既有安全字段必须逐项判断，不能机械删除。

- [ ] **Step 5: 提交文档与最终校验结果**

```bash
git add README_CN.md README.md docs/AI_CONTEXT.md docs/superpowers/specs/2026-08-08-feishu-codex-progress-card-relay-design.md
git diff --cached --name-status
git commit -m "docs: 说明飞书进度卡接力行为"
```

提交后再次运行：

```bash
git status --short --branch
git log -7 --oneline
```

Expected: 只剩用户原有 `tasks/lessons.md` 与 `.codex/` 状态；本功能提交完整且未推送。

---

## Task 8: 独立执行真实飞书端验收并保留发布门禁

**Files:**

- No source changes expected

- [ ] **Step 1: 在飞书桌面端验证卡片交互**

使用一个实际活动 Codex turn 验证：活动卡默认展开；手动折叠后持续收到至少两次进度更新仍保持折叠；手动重新展开可看到完整时间线。

- [ ] **Step 2: 在飞书移动端验证三次快速接力**

连续快速发送三条不同引导，逐条确认：

- Codex 实际收到三条引导且各一次。
- 每条消息下方各生成一张接力卡。
- 前三张旧权威卡依次显示“已转移”并折叠。
- 只有最后一张卡继续更新。
- 没有额外成功文字；失败路径仍有明确文字。

- [ ] **Step 3: 验证终态与恢复**

完成、停止和失败各验证一次最后权威卡自动折叠，最终结果仍只出现一次并保持独立。通过受控故障注入让一次旧卡 update 失败并重启 WeClaw，确认最新权威卡恢复、旧卡最终补偿为“已转移”。不得中断其他活动任务，不得使用 `--force`。

- [ ] **Step 4: 记录验收边界**

只有桌面端、移动端和重启补偿均实际通过后，才能把设计状态改为“已验收”。发布、push、`weclaw update` 和服务重启是另一个有远程副作用的阶段，必须按项目发布流程单独确认和执行。
