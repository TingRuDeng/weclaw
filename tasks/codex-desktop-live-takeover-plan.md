# Codex Desktop 实时接管 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让飞书和微信绑定并控制 Codex Desktop 当前持有的同一个 thread，继承其内存上下文、实时事件和待处理交互，同时阻止任何不确定状态下的双运行时写入。

**Architecture:** Agent 层新增 macOS Desktop IPC connector、conversation state projector 和 thread owner registry；`ACPAgent` 继续实现现有聊天与 thread 控制接口，但先按 owner 路由 Desktop 或 WeClaw app-server。Messaging 层只消费统一的 owner、状态和控制能力，保留现有任务卡片、暂存消息与平台鉴权，并用 rollout 处理 Desktop 断线后的只读终态观察。

**Tech Stack:** Go 1.26、Unix domain socket、4 字节小端长度帧、JSON envelope、Codex app-server 协议、现有 Messaging active-task 状态机、Go testing/race/vet/staticcheck。

---

## 0. 已冻结约束

### 0.1 领域契约

新增契约放在 `agent/codex_live_runtime.go`，不扩充已经较大的 `agent/agent.go`：

```go
type CodexRuntimeOwner string

const (
	CodexOwnerUnknown             CodexRuntimeOwner = "unknown"
	CodexOwnerDesktopLive         CodexRuntimeOwner = "desktop_live"
	CodexOwnerDesktopDisconnected CodexRuntimeOwner = "desktop_disconnected"
	CodexOwnerWeClawRuntime       CodexRuntimeOwner = "weclaw_runtime"
	CodexOwnerPersistedOnly       CodexRuntimeOwner = "persisted_only"
)

type CodexThreadRef struct {
	ConversationID string
	ThreadID       string
}

type CodexThreadBinding struct {
	Ref             CodexThreadRef
	Owner           CodexRuntimeOwner
	OwnerRevision   uint64
	Connected       bool
	ReleaseConfirmed bool
	State           CodexThreadState
}

type CodexLiveRuntimeAgent interface {
	BindCodexThread(context.Context, CodexThreadRef) (CodexThreadBinding, error)
	CurrentCodexThreadBinding(string) (CodexThreadBinding, bool)
	RecoverCodexThread(context.Context, CodexThreadRef) error
}
```

约束：

- `desktop_live`、`desktop_disconnected` 和 `unknown` 都不能执行 `thread/resume`。
- `persisted_only` 只表示 Agent 已确认 Desktop 未持有 thread；Messaging 还必须确认 rollout 已终态，才能调用 `RecoverCodexThread`。
- Desktop live binding 不写入 `ACPAgent.threads`；该 map 只表示当前 WeClaw app-server 已恢复或创建的 thread。
- `CurrentCodexThread` 先返回 live binding，再返回 ACP thread，保持现有会话记录逻辑可用。
- owner registry 按 thread 保存权威状态，conversation binding 只保存 route 到 thread 的选择。

### 0.2 错误契约

```go
var (
	ErrCodexDesktopUnavailable      = errors.New("Codex Desktop IPC unavailable")
	ErrCodexDesktopDisconnected     = errors.New("Codex Desktop IPC disconnected")
	ErrCodexDesktopOwnershipUnknown = errors.New("Codex Desktop thread ownership unknown")
	ErrCodexDesktopIncompatible     = errors.New("Codex Desktop IPC version incompatible")
	ErrCodexDesktopDeliveryUnknown  = errors.New("Codex Desktop request delivery is uncertain")
)
```

- socket 不存在且 Codex Desktop 进程不存在：能力不可用，可进入 `persisted_only`。
- socket 不存在但 Codex Desktop 进程仍存在、探测超时、写入后断线或远端显式业务错误：所有权或交付结果不确定，禁止本地重试。
- 只有 IPC router 明确返回 `no-client-found`，或收到 owner release/移除广播，或 socket 与 Desktop 进程均消失，才能记录 release evidence。

### 0.3 Wire 契约

```go
const (
	codexDesktopFrameHeaderBytes = 4
	codexDesktopMaxFrameBytes    = 256 << 20
	codexDesktopRequestTimeout   = 10 * time.Second
	codexDesktopOwnershipTimeout = 1500 * time.Millisecond
)

var codexDesktopMethodVersions = map[string]int{
	"initialize":                                        1,
	"thread-stream-state-changed":                       11,
	"thread-follower-load-complete-history":             1,
	"thread-follower-start-turn":                        1,
	"thread-follower-steer-turn":                        1,
	"thread-follower-interrupt-turn":                    2,
	"thread-follower-command-approval-decision":         1,
	"thread-follower-file-approval-decision":            1,
	"thread-follower-submit-user-input":                  1,
	"thread-follower-permissions-request-approval-response": 1,
}
```

首期实际发送规则：

- `turn/start` → `{conversationId, senderRequestId, turnStartParams}`。
- `turn/steer` → `{conversationId, input, expectedTurnId}`。
- `turn/interrupt` → `{conversationId, turnId}`。
- 命令审批 → `thread-follower-command-approval-decision`。
- 文件修改、文件读取和权限请求 → 已验证的 `thread-follower-file-approval-decision`。
- 结构化问答 → `{conversationId, requestId, response:{answers}}`。
- 专用 permissions response 方法只保留在版本表和 golden fixture 中；没有实机证据前不作为发送路径。

### 0.4 文件结构

新增 Agent 文件：

| 文件 | 单一职责 |
|---|---|
| `agent/codex_live_runtime.go` | 公共 owner、binding、错误和可选能力接口。 |
| `agent/codex_desktop_frame.go` | 长度前缀 frame 读写。 |
| `agent/codex_desktop_protocol.go` | envelope、method 版本、wire DTO 与校验。 |
| `agent/codex_desktop_client.go` | 连接、initialize、请求关联和重连生命周期。 |
| `agent/codex_desktop_client_read.go` | frame read loop、response/discovery/broadcast 分派。 |
| `agent/codex_desktop_endpoint_darwin.go` | 当前用户 socket 路径、uid/mode 校验和 Codex 进程探测。 |
| `agent/codex_desktop_endpoint_other.go` | 非 macOS 显式不可用实现。 |
| `agent/codex_desktop_patch.go` | Immer `add/replace/remove` patch 与 revision 校验。 |
| `agent/codex_desktop_state.go` | snapshot 基线、线程缓存、订阅和断线状态。 |
| `agent/codex_desktop_projection.go` | turn/item/text/progress/终态投影。 |
| `agent/codex_desktop_actions.go` | start/steer/interrupt/审批/问答 wire 映射。 |
| `agent/codex_desktop_owner.go` | thread owner 状态机、release evidence 和 route binding。 |
| `agent/codex_desktop_runtime.go` | Desktop Chat/Watch/Read/Steer/Interrupt 统一入口。 |
| `agent/codex_runtime_recovery.go` | ACP 进程安全刷新并恢复同一 thread。 |
| `agent/user_input.go` | 结构化用户问答及 context handler。 |

新增 Messaging 文件：

| 文件 | 单一职责 |
|---|---|
| `messaging/codex_runtime_binding.go` | owner-aware bind/recover 决策，供切换、普通消息和广播复用。 |
| `messaging/codex_external_watch.go` | Desktop watcher 断线后切换 rollout，只在真实终态完成任务。 |
| `messaging/agent_interactions.go` | 同一任务 context 注入审批和结构化问答处理器。 |
| `messaging/codex_live_fakes_test.go` | 新 owner/runtime fake，避免继续扩充历史 fake 文件。 |

测试均使用独立新文件；不继续扩充已超过或接近 300 行的历史测试文件。

## Task 1: 固化 Desktop IPC frame 与 envelope

**Files:**
- Create: `agent/codex_desktop_frame.go`
- Create: `agent/codex_desktop_protocol.go`
- Create: `agent/codex_desktop_frame_test.go`
- Create: `agent/codex_desktop_protocol_test.go`
- Create: `agent/testdata/codex_desktop/initialize_request.json`
- Create: `agent/testdata/codex_desktop/thread_snapshot.json`
- Create: `agent/testdata/codex_desktop/thread_patches.json`

- [ ] **Step 1: 写 frame 失败测试**

```go
func TestCodexDesktopFrameReadsFragmentedPayload(t *testing.T) {}
func TestCodexDesktopFrameReadsCoalescedFrames(t *testing.T) {}
func TestCodexDesktopFrameRejectsOversizedPayload(t *testing.T) {}
func TestCodexDesktopFrameRejectsTruncatedPayload(t *testing.T) {}
```

测试使用 `io.Pipe` 分段写 header/payload，并验证连续两帧不会互相吞并；超限在分配 payload 前失败，EOF 半包返回带 frame 上下文的错误。

- [ ] **Step 2: 运行失败测试**

Run: `go test ./agent -run 'TestCodexDesktopFrame' -count=1 -timeout 60s`

Expected: FAIL，提示 `readCodexDesktopFrame` / `writeCodexDesktopFrame` 未定义。

- [ ] **Step 3: 实现 frame codec**

```go
func readCodexDesktopFrame(r io.Reader) ([]byte, error)
func writeCodexDesktopFrame(w io.Writer, payload []byte) error
```

使用 `binary.LittleEndian` 和 `io.ReadFull`；空 JSON frame、超限长度和截断 payload 都显式报错，不吞非法数据继续读。

- [ ] **Step 4: 写 envelope 与版本失败测试**

```go
func TestCodexDesktopEnvelopeMatchesGoldenInitialize(t *testing.T) {}
func TestCodexDesktopEnvelopeRejectsUnknownBroadcastVersion(t *testing.T) {}
func TestCodexDesktopEnvelopeRejectsInvalidJSON(t *testing.T) {}
func TestCodexDesktopDiscoveryCarriesNestedMethodVersion(t *testing.T) {}
```

golden fixture 使用本机只读探测和 Remodex 当前实现已验证的字段，只保留协议形状，不复制上游实现代码。

- [ ] **Step 5: 实现 wire DTO 和校验**

```go
type codexDesktopEnvelope struct {
	Type             string          `json:"type"`
	RequestID        string          `json:"requestId,omitempty"`
	SourceClientID   string          `json:"sourceClientId,omitempty"`
	Version          int             `json:"version,omitempty"`
	Method           string          `json:"method,omitempty"`
	Params           json.RawMessage `json:"params,omitempty"`
	ResultType       string          `json:"resultType,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            string          `json:"error,omitempty"`
	Request          json.RawMessage `json:"request,omitempty"`
	Response         json.RawMessage `json:"response,omitempty"`
}
```

只接受 `thread-stream-state-changed@11`；未知 method 可以忽略并限频记录，已知 method 的错误版本必须返回 `ErrCodexDesktopIncompatible`。

- [ ] **Step 6: 验证并提交**

Run: `gofmt -w agent/codex_desktop_frame.go agent/codex_desktop_protocol.go agent/codex_desktop_frame_test.go agent/codex_desktop_protocol_test.go`

Run: `go test ./agent -run 'TestCodexDesktop(Frame|Envelope|Discovery)' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add agent/codex_desktop_frame.go agent/codex_desktop_protocol.go agent/codex_desktop_frame_test.go agent/codex_desktop_protocol_test.go agent/testdata/codex_desktop
git commit -m "实现 Codex Desktop IPC 协议帧"
```

## Task 2: 建立安全的 IPC client 与 macOS endpoint

**Files:**
- Create: `agent/codex_desktop_client.go`
- Create: `agent/codex_desktop_client_read.go`
- Create: `agent/codex_desktop_client_test.go`
- Create: `agent/codex_desktop_endpoint_darwin.go`
- Create: `agent/codex_desktop_endpoint_other.go`
- Create: `agent/codex_desktop_endpoint_darwin_test.go`
- Create: `agent/codex_desktop_endpoint_other_test.go`

- [ ] **Step 1: 写 endpoint 安全失败测试**

```go
func TestCodexDesktopEndpointUsesCurrentUIDSocket(t *testing.T) {}
func TestCodexDesktopEndpointRejectsNonSocket(t *testing.T) {}
func TestCodexDesktopEndpointRejectsDifferentUID(t *testing.T) {}
func TestCodexDesktopPresenceRequiresMissingSocketAndProcess(t *testing.T) {}
```

Darwin 测试带 `//go:build darwin`，默认路径必须等于 `filepath.Join(os.TempDir(), "codex-ipc", fmt.Sprintf("ipc-%d.sock", os.Getuid()))`；聊天输入不能覆盖路径。进程探测使用 `unix.SysctlKinfoProcSlice("kern.proc.all")`，只识别主进程 `Codex`，测试通过依赖注入模拟。非 Darwin 测试验证 connector 显式返回 `ErrCodexDesktopUnavailable`，保证 Linux/Windows 全仓测试可编译。

- [ ] **Step 2: 写 client 生命周期失败测试**

```go
func TestCodexDesktopClientInitializesBeforeRequests(t *testing.T) {}
func TestCodexDesktopClientCorrelatesConcurrentResponses(t *testing.T) {}
func TestCodexDesktopClientFailsPendingCallsOnDisconnect(t *testing.T) {}
func TestCodexDesktopClientReturnsDeliveryUnknownAfterWrite(t *testing.T) {}
func TestCodexDesktopClientDoesNotRetryAmbiguousStartTurn(t *testing.T) {}
func TestCodexDesktopClientAnswersDiscoveryFalse(t *testing.T) {}
```

测试用 `net.Pipe` 模拟 router；断线前未完成 write 返回 delivery failure，write 成功后超时/断线返回 `ErrCodexDesktopDeliveryUnknown`。

- [ ] **Step 3: 实现 client 接口和状态**

```go
type codexDesktopClient struct {
	mu        sync.Mutex
	dial      func(context.Context) (net.Conn, error)
	conn      net.Conn
	clientID  string
	epoch     uint64
	pending   map[string]*codexDesktopPendingCall
	discovery map[string]*codexDesktopPendingDiscovery
	onBroadcast func(codexDesktopEnvelope)
}

func (c *codexDesktopClient) Connect(context.Context) error
func (c *codexDesktopClient) Call(context.Context, string, any) (json.RawMessage, error)
func (c *codexDesktopClient) Discover(context.Context, codexDesktopRequest) (bool, error)
func (c *codexDesktopClient) Close() error
```

`Connect` 完成 `initialize@1` 后才发布 connected；`initialize` 参数为 `{clientType:"weclaw"}`，成功结果必须含 `clientId`。read loop 独占读取 conn，write 使用互斥锁，pending channel 带 1 个缓冲，关闭时一次性失败全部 waiter。

- [ ] **Step 4: 实现 discovery 和显式 delivery 分类**

收到 peer `client-discovery-request` 时第一阶段始终返回 `canHandle=false`。`Call` 只把 router 的 `no-client-found` 标记为“确认未送达”；timeout、远端 error 和 write 后 close 都不允许调用者重试 mutating request。

- [ ] **Step 5: 验证并提交**

Run: `gofmt -w agent/codex_desktop_client*.go agent/codex_desktop_endpoint*.go`

Run: `go test ./agent -run 'TestCodexDesktop(Client|Endpoint|Presence)' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add agent/codex_desktop_client.go agent/codex_desktop_client_read.go agent/codex_desktop_client_test.go agent/codex_desktop_endpoint_darwin.go agent/codex_desktop_endpoint_other.go agent/codex_desktop_endpoint_darwin_test.go agent/codex_desktop_endpoint_other_test.go
git commit -m "建立 Codex Desktop IPC 安全连接"
```

## Task 3: 实现 snapshot、patch 与事件投影

**Files:**
- Create: `agent/codex_desktop_patch.go`
- Create: `agent/codex_desktop_patch_test.go`
- Create: `agent/codex_desktop_state.go`
- Create: `agent/codex_desktop_state_test.go`
- Create: `agent/codex_desktop_projection.go`
- Create: `agent/codex_desktop_projection_test.go`
- Modify: `agent/agent.go`

- [x] **Step 1: 写 patch 与 revision 失败测试**

```go
func TestCodexDesktopPatchAppliesAddReplaceRemove(t *testing.T) {}
func TestCodexDesktopPatchRejectsInvalidPathAndIndex(t *testing.T) {}
func TestCodexDesktopStateRejectsPatchWithoutBaseline(t *testing.T) {}
func TestCodexDesktopStateRejectsRevisionGap(t *testing.T) {}
func TestCodexDesktopStateDeduplicatesRevision(t *testing.T) {}
```

Immer path 同时覆盖对象 key 和数组 index；root 只允许 `add/replace`。patch 必须满足 `baseRevision == currentRevision`，重复或旧 revision 不产生事件，断档进入 `needsSnapshot`。

- [x] **Step 2: 实现原子状态缓存**

```go
type codexDesktopThreadSnapshot struct {
	ThreadID        string
	ConnectionEpoch uint64
	Revision        uint64
	Raw             map[string]any
	State           CodexThreadState
	Requests        map[string]codexDesktopPendingAction
	UpdatedAt       time.Time
}
```

snapshot 深拷贝后原子替换；patch 在私有副本上全部成功才提交。缺基线或断档时调用 `thread-follower-load-complete-history {conversationId}`，只在对应 revision 的 snapshot 到达后恢复 live 控制。

同时给 `CodexThreadState` 增加 `Model` 和 `Effort`，由 snapshot 的 `latestModel`、`latestReasoningEffort` 投影；后续切换反馈直接复用该结构，不在 Messaging 解析原始 Desktop JSON。

- [x] **Step 3: 写 turn/item 投影失败测试**

```go
func TestCodexDesktopProjectionFindsExplicitActiveTurn(t *testing.T) {}
func TestCodexDesktopProjectionDoesNotTreatUnknownStatusAsActive(t *testing.T) {}
func TestCodexDesktopProjectionEmitsTextSuffixOnly(t *testing.T) {}
func TestCodexDesktopProjectionRebuildsRewrittenText(t *testing.T) {}
func TestCodexDesktopProjectionEmitsTerminalOncePerTurn(t *testing.T) {}
func TestCodexDesktopProjectionKeepsParallelSiblingTurnsSeparate(t *testing.T) {}
func TestCodexDesktopStateEvictsOnlyIdleThreads(t *testing.T) {}
```

明确 active 状态仅接受 `inProgress/running/active/processing`；终态按 turn ID 去重。相同 item 文本只在新值以前值为前缀时发 delta，文本改写生成 snapshot event，不把整段重复追加。

- [x] **Step 4: 复用现有 `codexTurnEvent`**

本任务投影 `started/progress/item_completed/completed/error`，并把未完成 request 保存在结构化 pending action map；审批和用户问答事件在 Task 4 增加 responder 后再投递。进度继续使用 `codexProgressEvent`，最终文本继续交给 `codexFinalAssembler`。缓存上限使用具名常量：512 threads、每 thread 200 turns、300 queued patches；active 或 pending request thread 不参与淘汰。

- [x] **Step 5: 验证并提交**

Run: `gofmt -w agent/codex_desktop_patch*.go agent/codex_desktop_state*.go agent/codex_desktop_projection*.go agent/agent.go`

Run: `go test ./agent -run 'TestCodexDesktop(Patch|State|Projection)' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add agent/codex_desktop_patch.go agent/codex_desktop_patch_test.go agent/codex_desktop_state.go agent/codex_desktop_state_test.go agent/codex_desktop_projection.go agent/codex_desktop_projection_test.go agent/agent.go
git commit -m "投影 Codex Desktop 会话实时状态"
```

## Task 4: 映射 Desktop 操作、审批和结构化问答

**Files:**
- Create: `agent/codex_desktop_actions.go`
- Create: `agent/codex_desktop_actions_test.go`
- Create: `agent/user_input.go`
- Create: `agent/user_input_test.go`
- Modify: `agent/acp_types.go`
- Modify: `agent/acp_permission_bridge.go`
- Modify: `agent/acp_chat.go`
- Modify: `agent/codex_app_server_turn.go`
- Modify: `agent/codex_thread_watch.go`
- Modify: `agent/codex_turn_dispatch.go`

- [x] **Step 1: 写 turn 操作 wire 失败测试**

```go
func TestCodexDesktopStartTurnMapsFollowerPayload(t *testing.T) {}
func TestCodexDesktopSteerMapsExpectedTurn(t *testing.T) {}
func TestCodexDesktopInterruptUsesVersionTwo(t *testing.T) {}
func TestCodexDesktopStartDoesNotRetryDeliveryUnknown(t *testing.T) {}
```

`senderRequestId` 在一次逻辑发送中稳定；start 响应兼容 `{result:{turn:{id}}}` 和 `{turn:{id}}`，两种都必须提取非空 turn ID。

- [x] **Step 2: 把审批响应改成 provider responder**

```go
type codexApprovalRequest struct {
	ID      json.RawMessage
	Request ApprovalRequest
	Respond func(context.Context, string) error
}
```

app-server 在 `handlePermissionRequest` 构造 responder，内部继续调用 `respondPermissionRequest`；Desktop projector 构造 follower responder。`chatLegacyACP`、`chatCodexAppServerWithRetry` 和 `collectAttachedCodexTurn` 统一调用新的 `handleCodexApprovalEvent`：执行 `resolvePermissionOption` 后调用 `evt.Approval.Respond`，没有 responder 必须报错，不能自动批准。`isCodexTurnControlEvent` 同时把审批和用户问答视为不可丢控制事件。

- [x] **Step 3: 写审批映射失败测试**

```go
func TestCodexDesktopCommandApprovalUsesCommandDecision(t *testing.T) {}
func TestCodexDesktopFileAndPermissionApprovalUseFileDecision(t *testing.T) {}
func TestCodexDesktopApprovalRejectsUnknownDecision(t *testing.T) {}
func TestCodexDesktopPendingApprovalSurvivesDisconnect(t *testing.T) {}
func TestCodexDesktopResolvedApprovalIsNotReplayed(t *testing.T) {}
```

允许的 decision 仅为 `accept/acceptForSession/decline/cancel`；未知值返回错误并保留 pending，不默认批准。

- [x] **Step 4: 定义结构化问答契约**

```go
type UserInputOption struct { Label, Description string }
type UserInputQuestion struct { ID, Header, Prompt string; Options []UserInputOption }
type UserInputRequest struct { RequestID string; Questions []UserInputQuestion }
type UserInputAnswers map[string][]string
type UserInputHandler func(context.Context, UserInputRequest) (UserInputAnswers, error)

func ContextWithUserInputHandler(context.Context, UserInputHandler) context.Context
```

Desktop `item/tool/requestUserInput` 只在 request 未 completed 且 questions 非空时投影。回答必须覆盖每个 question ID，并发送 `thread-follower-submit-user-input`；空答案保持 pending 并显式失败。

- [x] **Step 5: 验证并提交**

Run: `gofmt -w agent/codex_desktop_actions*.go agent/user_input*.go agent/acp_types.go agent/acp_permission_bridge.go agent/acp_chat.go agent/codex_app_server_turn.go agent/codex_thread_watch.go agent/codex_turn_dispatch.go`

Run: `go test ./agent -run 'Test(CodexDesktop(Start|Steer|Interrupt|Command|File|Approval|Pending|Resolved)|UserInput)' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add agent/codex_desktop_actions.go agent/codex_desktop_actions_test.go agent/user_input.go agent/user_input_test.go agent/acp_types.go agent/acp_permission_bridge.go agent/acp_chat.go agent/codex_app_server_turn.go agent/codex_thread_watch.go agent/codex_turn_dispatch.go
git commit -m "接入 Codex Desktop 控制与交互请求"
```

## Task 5: 建立 thread owner 状态机

**Files:**
- Create: `agent/codex_live_runtime.go`
- Create: `agent/codex_desktop_owner.go`
- Create: `agent/codex_desktop_owner_test.go`
- Modify: `agent/acp_agent.go`
- Modify: `agent/acp_constructor.go`
- Modify: `agent/acp_types.go`
- Modify: `agent/acp_state.go`
- Test: `agent/acp_recovery_test.go`

- [x] **Step 1: 写 owner 转移失败测试**

```go
func TestCodexDesktopSnapshotClaimsUnownedThread(t *testing.T) {}
func TestCodexDesktopSnapshotDoesNotStealActiveWeClawThread(t *testing.T) {}
func TestCodexRuntimeOwnerDisconnectDoesNotReleaseThread(t *testing.T) {}
func TestCodexRuntimeOwnerDiscoveryTimeoutRemainsUnknown(t *testing.T) {}
func TestCodexRuntimeOwnerNoClientFoundConfirmsRelease(t *testing.T) {}
func TestCodexRuntimeOwnerMissingSocketAndProcessConfirmsRelease(t *testing.T) {}
```

- [x] **Step 2: 实现 `BindCodexThread` 的只读探测顺序**

```text
缓存 snapshot 命中 -> desktop_live
没有 snapshot -> discovery(load-complete-history@1)
discovery=true -> 发送 load-complete-history 并等待对应 snapshot
discovery=false/timeout -> 仍发送只读 load-complete-history，以 router 结果确认是否无人持有
load-complete-history 返回 no-client-found -> release_confirmed
socket 与进程都不存在 -> release_confirmed
load 请求 timeout/断线/业务 error -> unknown 或 desktop_disconnected
```

探测期间不发送 turn/start，也不调用 ACP。第一阶段收到其他 client discovery 一律答 false，避免声称自己实现了 owner 广播处理器。

- [x] **Step 3: 注入 connector 而不改变 `NewACPAgent` 外部签名**

`ACPAgent` 新增包内 `desktopRuntime codexDesktopRuntime` 和 owner registry；仅 `protocolCodexAppServer` 构造。测试通过包内 constructor options 注入 fake，不依赖真实 Codex App。

- [x] **Step 4: 持久化不确定 live binding**

`acpPersistedState` 版本升到 2，新增 conversation→thread live binding。落盘时 `desktop_live` 统一写成 `desktop_disconnected`；加载后必须重新 probe，不能直接恢复控制或 ACP thread。读取 v1 state 保持现有 sessions/threads/history 迁移。

- [x] **Step 5: 验证并提交**

Run: `gofmt -w agent/codex_live_runtime.go agent/codex_desktop_owner*.go agent/acp_agent.go agent/acp_constructor.go agent/acp_types.go agent/acp_state.go agent/acp_recovery_test.go`

Run: `go test ./agent -run 'TestCodex(RuntimeOwner|DesktopSnapshot)|TestACPState' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add agent/codex_live_runtime.go agent/codex_desktop_owner.go agent/codex_desktop_owner_test.go agent/acp_agent.go agent/acp_constructor.go agent/acp_types.go agent/acp_state.go agent/acp_recovery_test.go
git commit -m "建立 Codex thread 单一运行时所有权"
```

## Task 6: 让 ACPAgent 按 owner 路由 Chat 和 thread 控制

**Files:**
- Create: `agent/codex_desktop_runtime.go`
- Create: `agent/codex_desktop_runtime_test.go`
- Modify: `agent/acp_chat.go`
- Modify: `agent/acp_threads.go`
- Modify: `agent/codex_thread_state.go`
- Modify: `agent/codex_thread_watch.go`

- [x] **Step 1: 写 Desktop Chat 路由失败测试**

```go
func TestACPAgentDesktopChatDoesNotStartAppServer(t *testing.T) {}
func TestACPAgentDesktopChatSubscribesBeforeStart(t *testing.T) {}
func TestACPAgentDesktopChatKeepsSameThreadContext(t *testing.T) {}
func TestACPAgentDesktopWatchUsesProjectedEvents(t *testing.T) {}
```

fake 的 `Start` 调用即失败，以证明 `ACPAgent.chat` 在 `isRuntimeStarted` 前先检查 Desktop binding。

- [x] **Step 2: 实现 owner-first Chat**

```go
func (a *ACPAgent) chat(ctx context.Context, conversationID, message string, onProgress func(string)) (string, error) {
	if a.protocol == protocolCodexAppServer {
		if binding, ok := a.CurrentCodexThreadBinding(conversationID); ok {
			switch binding.Owner {
			case CodexOwnerDesktopLive:
				return a.chatCodexDesktop(ctx, binding, message, onProgress)
			case CodexOwnerDesktopDisconnected:
				return "", ErrCodexDesktopDisconnected
			case CodexOwnerUnknown:
				return "", ErrCodexDesktopOwnershipUnknown
			case CodexOwnerPersistedOnly:
				return "", fmt.Errorf("Codex thread must be recovered before chat")
			}
		}
	}
	if !a.isRuntimeStarted() {
		if err := a.Start(ctx); err != nil { return "", err }
	}
	if a.protocol == protocolCodexAppServer {
		return a.chatCodexAppServer(ctx, conversationID, message, onProgress)
	}
	return a.chatLegacyACP(ctx, conversationID, message, onProgress, true)
}
```

Desktop 路径先订阅 thread，再发送 start；start 返回的 turn ID 与 snapshot turn ID 关联后，复用 `collectAttachedCodexTurn` 聚合结果。

- [x] **Step 3: 写控制路由失败测试**

```go
func TestACPAgentDesktopReadStateDoesNotCallThreadRead(t *testing.T) {}
func TestACPAgentDesktopControlsUseFollowerMethods(t *testing.T) {}
func TestACPAgentDisconnectedControlsReturnTypedError(t *testing.T) {}
func TestACPAgentDesktopApprovalUsesContextHandler(t *testing.T) {}
func TestACPAgentDesktopUserInputUsesContextHandler(t *testing.T) {}
```

- [x] **Step 4: 拆出 app-server 私有实现**

保留 `CodexThreadRuntimeAgent` 的四个公开签名；新增 `readCodexAppServerThreadState`、`steerCodexAppServerThread`、`interruptCodexAppServerThread`、`watchCodexAppServerThread`。公开方法只按 binding owner 分派，不在 `codex_thread_state.go` 混入 IPC 解析。

- [x] **Step 5: 验证并提交**

Run: `gofmt -w agent/codex_desktop_runtime*.go agent/acp_chat.go agent/acp_threads.go agent/codex_thread_state.go agent/codex_thread_watch.go`

Run: `go test ./agent -run 'TestACPAgentDesktop' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add agent/codex_desktop_runtime.go agent/codex_desktop_runtime_test.go agent/acp_chat.go agent/acp_threads.go agent/codex_thread_state.go agent/codex_thread_watch.go
git commit -m "按 owner 路由 Codex Desktop 实时会话"
```

## Task 7: 安全刷新 ACP 并恢复同一 thread

**Files:**
- Create: `agent/codex_runtime_recovery.go`
- Create: `agent/codex_runtime_recovery_test.go`
- Modify: `agent/acp_process.go`
- Modify: `agent/acp_threads.go`
- Modify: `agent/turn_channel_registry.go`

- [x] **Step 1: 写恢复门禁失败测试**

```go
func TestACPAgentRecoverCodexThreadRejectsLiveOwner(t *testing.T) {}
func TestACPAgentRecoverCodexThreadRejectsDisconnectedOwner(t *testing.T) {}
func TestACPAgentRecoverCodexThreadRejectsActiveACPTurn(t *testing.T) {}
func TestACPAgentRecoverCodexThreadRestartsBeforeResume(t *testing.T) {}
func TestACPAgentRestoredThreadResumeFailureIsReturned(t *testing.T) {}
func TestACPAgentRecoveryDoesNotFailDesktopWatchers(t *testing.T) {}
```

- [x] **Step 2: 对恢复目标采用保守的 process 刷新策略**

不依赖无法从 Desktop IPC 完整证明的 process epoch。只允许恢复 `persisted_only` 且已确认释放的 thread，并在恢复前确认 ACP `turnCh` 中没有 active turn；每次恢复目标 thread 都刷新 app-server，确保不会复用已加载的陈旧上下文。

- [x] **Step 3: 实现 process-only restart**

整体 `Stop` 仍关闭所有 runtime；内部 `restartCodexAppServer` 只停止 ACP subprocess、失败 app-server pending RPC/turn，不能关闭 Desktop state subscriptions。新进程启动后将其他 ACP conversation 标回 `resumeOnFirstUse`，目标 thread 执行一次真实 `thread/resume`。

- [x] **Step 4: 修复已存在的吞错行为**

`getOrCreateThread` 的 restored `thread/resume` 失败必须返回 error，不再只写日志后继续在旧 ID 上 `turn/start`。`UseCodexThread` 改为 owner-aware wrapper，Messaging 新代码调用 `RecoverCodexThread`。

- [x] **Step 5: 验证并提交**

Run: `gofmt -w agent/codex_runtime_recovery*.go agent/acp_process.go agent/acp_threads.go agent/turn_channel_registry.go`

Run: `go test ./agent -run 'TestACPAgentRecover|TestACPAgentRestoredThread' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add agent/codex_runtime_recovery.go agent/codex_runtime_recovery_test.go agent/acp_process.go agent/acp_threads.go agent/turn_channel_registry.go
git commit -m "安全恢复 Codex 持久化 thread"
```

## Task 8: 为 Messaging 注入审批和结构化问答

**Files:**
- Create: `messaging/agent_interactions.go`
- Create: `messaging/handler_user_input_test.go`
- Modify: `messaging/codex_agent_task.go`
- Modify: `messaging/codex_external_task.go`
- Modify: `messaging/agent_broadcast.go`
- Modify: `messaging/approvals.go`

- [x] **Step 1: 写问答卡片失败测试**

```go
func TestUserInputHandlerCollectsEachQuestionAnswer(t *testing.T) {}
func TestUserInputHandlerRejectsQuestionWithoutOptions(t *testing.T) {}
func TestUserInputHandlerRejectsUnauthorizedRoute(t *testing.T) {}
func TestUserInputHandlerCancelsOutstandingChoicesOnContextDone(t *testing.T) {}
```

每个 question 通过现有 `AskChoices` 顺序展示，内部 decision key 使用 `requestID:questionID`，返回 option label 组成 `UserInputAnswers`。无 options 的请求首期显式提示“不支持自由文本问答”，不劫持普通消息作为隐式回答。

- [x] **Step 2: 提取统一 context 注入函数**

```go
type agentInteractionContextOptions struct {
	actorUserID string
	routeUserID string
	reply       platform.Replier
}

func (h *Handler) withAgentInteractions(
	ctx context.Context,
	opts agentInteractionContextOptions,
) context.Context
```

该函数同时调用 `ContextWithApprovalHandler` 和 `ContextWithUserInputHandler`；普通 Codex 任务、外部 Desktop watcher 和广播入口必须复用它，避免某一平台漏掉交互处理。

- [x] **Step 3: 验证并提交**

Run: `gofmt -w messaging/agent_interactions.go messaging/handler_user_input_test.go messaging/codex_agent_task.go messaging/codex_external_task.go messaging/agent_broadcast.go messaging/approvals.go`

Run: `go test ./messaging -run 'TestUserInputHandler' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add messaging/agent_interactions.go messaging/handler_user_input_test.go messaging/codex_agent_task.go messaging/codex_external_task.go messaging/agent_broadcast.go messaging/approvals.go
git commit -m "支持远程回答 Codex 结构化问题"
```

## Task 9: 让会话切换先绑定 owner

**Files:**
- Create: `messaging/codex_runtime_binding.go`
- Create: `messaging/codex_live_fakes_test.go`
- Create: `messaging/handler_codex_live_switch_test.go`
- Modify: `messaging/codex_task_types.go`
- Modify: `messaging/codex_session_switch.go`
- Modify: `messaging/codex_browser.go`
- Modify: `messaging/agent_conversation.go`
- Modify: `messaging/agent_broadcast.go`

- [x] **Step 1: 写切换路径失败测试**

```go
func TestCodexSwitchDesktopActiveSkipsUseCodexThread(t *testing.T) {}
func TestCodexSwitchDesktopIdleSkipsUseCodexThread(t *testing.T) {}
func TestCodexSingleSessionAutoSwitchDesktopOwnerSkipsUseCodexThread(t *testing.T) {}
func TestCodexSwitchUnknownOwnerDoesNotResumeACP(t *testing.T) {}
func TestCodexSwitchBlocksDifferentThreadWhileTaskRuns(t *testing.T) {}
func TestCodexPrepareConversationRechecksOwnerEveryTurn(t *testing.T) {}
func TestCodexBroadcastUsesOwnerAwareBinding(t *testing.T) {}
```

- [x] **Step 2: 实现统一 bind/recover resolver**

```go
type codexRuntimeResolution struct {
	Binding agent.CodexThreadBinding
	Rollout codexRolloutTaskState
}

type codexRuntimeResolveOptions struct {
	route    codexConversationRoute
	threadID string
	ag       agent.Agent
}

func (h *Handler) resolveCodexRuntime(
	ctx context.Context,
	opts codexRuntimeResolveOptions,
) (codexRuntimeResolution, error)
```

规则：

1. 调用 `BindCodexThread`。
2. `desktop_live`：直接返回，绝不调用 ACP resume。
3. `desktop_disconnected/unknown`：读取 rollout 供只读状态，不恢复。
4. `persisted_only`：rollout active 时仍保持 disconnected；rollout 终态且 route 无 active task 时调用 `RecoverCodexThread`。
5. 不支持新接口的测试/非 app-server fake 才沿用现有 `UseCodexThread`。

- [x] **Step 3: 替换三个恢复入口**

`handleCodexSwitchForRouteWithOptions`、`enterCodexWorkspaceWithSingleSession`、`prepareCodexConversation` 全部调用 resolver；`agent_broadcast` 走相同路径。`codexConversationRoute` 增加冻结的 `threadID`，暂存消息不能在执行时悄悄换成别的 thread。

- [x] **Step 4: 切换反馈包含 live 状态**

使用 Task 3 已增加的 `CodexThreadState.Model` 和 `Effort`；Desktop snapshot 优先展示，缺失时再使用 `codexSessionModelStatus`。active thread 返回任务、当前进展和可用控制；idle thread 返回当前模型与推理强度。

- [x] **Step 5: 验证并提交**

Run: `gofmt -w messaging/codex_runtime_binding.go messaging/codex_live_fakes_test.go messaging/handler_codex_live_switch_test.go messaging/codex_task_types.go messaging/codex_session_switch.go messaging/codex_browser.go messaging/agent_conversation.go messaging/agent_broadcast.go`

Run: `go test ./messaging -run 'TestCodex(Switch|SingleSession|PrepareConversation|Broadcast).*Owner|TestCodexSwitchBlocks' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add messaging/codex_runtime_binding.go messaging/codex_live_fakes_test.go messaging/handler_codex_live_switch_test.go messaging/codex_task_types.go messaging/codex_session_switch.go messaging/codex_browser.go messaging/agent_conversation.go messaging/agent_broadcast.go
git commit -m "会话切换绑定 Codex 实时运行时"
```

## Task 10: 重构 active task owner 与终态幂等

**Files:**
- Create: `messaging/handler_codex_live_task_state_test.go`
- Modify: `messaging/task_state.go`
- Modify: `messaging/task_external_control.go`
- Modify: `messaging/codex_agent_task.go`
- Modify: `messaging/codex_task_types.go`

- [x] **Step 1: 写任务状态失败测试**

```go
func TestCodexTaskStoresOwnerRevisionAndThread(t *testing.T) {}
func TestCodexTaskTerminalCanOnlyBeClaimedOnce(t *testing.T) {}
func TestCodexPendingMessageKeepsFrozenThreadRoute(t *testing.T) {}
func TestCodexStopPhaseKeepsPendingMessage(t *testing.T) {}
```

- [x] **Step 2: 用具名状态替换两个布尔字段**

```go
type codexTaskPhase string

const (
	codexTaskRunning      codexTaskPhase = "running"
	codexTaskStopping     codexTaskPhase = "stopping"
	codexTaskDisconnected codexTaskPhase = "disconnected"
	codexTaskTerminal     codexTaskPhase = "terminal"
)
```

`activeAgentTask` 保存 `runtimeOwner`、`ownerRevision`、`phase`、`codexThreadID`、`codexTurnID`，删除 `externalCodex/externalControl`。`claimTerminal` 在 task mutex 下只允许第一次从非终态进入终态；IPC 与 rollout 只有获胜者可以发送最终结果和提升 pending。

- [x] **Step 3: 暂存消息执行前重新 bind**

`pendingCodexTask` 继续冻结 route/thread/reply，但 `runCodexAgentTask` 必须再次执行 Task 9 resolver；不得捕获旧 owner 或直接调用 Desktop/ACP。

- [x] **Step 4: 验证并提交**

Run: `gofmt -w messaging/handler_codex_live_task_state_test.go messaging/task_state.go messaging/task_external_control.go messaging/codex_agent_task.go messaging/codex_task_types.go`

Run: `go test ./messaging -run 'TestCodexTask|TestCodexPendingMessageKeeps' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add messaging/handler_codex_live_task_state_test.go messaging/task_state.go messaging/task_external_control.go messaging/codex_agent_task.go messaging/codex_task_types.go
git commit -m "统一 Codex 实时任务所有权状态"
```

## Task 11: Desktop 断线时接续 rollout 而不误完成任务

**Files:**
- Create: `messaging/codex_external_watch.go`
- Create: `messaging/handler_codex_live_recovery_test.go`
- Create: `messaging/handler_codex_live_progress_test.go`
- Modify: `messaging/codex_external_task.go`
- Modify: `messaging/codex_rollout_watch.go`

- [x] **Step 1: 写断线与双源终态失败测试**

```go
func TestCodexDesktopDisconnectDoesNotFinishTask(t *testing.T) {}
func TestCodexDesktopDisconnectDoesNotRunPendingMessage(t *testing.T) {}
func TestCodexDesktopDisconnectRebootsRolloutFromCurrentTail(t *testing.T) {}
func TestCodexDesktopProgressSurvivesRolloutHandoff(t *testing.T) {}
func TestCodexDesktopTerminalDeliveredOnce(t *testing.T) {}
func TestCodexPendingWaitsForDesktopRelease(t *testing.T) {}
func TestCodexReconnectRestoresControlAfterSnapshot(t *testing.T) {}
```

- [x] **Step 2: 把 watcher 结果分类为终态或观察中断**

```go
type codexExternalWatchResult struct {
	Final    string
	Terminal bool
	Failed   bool
	Source   string
}
```

`ErrCodexDesktopDisconnected` 只把 task phase 改为 disconnected；立即重新调用 `readLocalCodexRolloutTaskState`，使用新的 Path/TurnID/Offset bootstrap rollout，不能沿用 IPC 静音期间越过的旧 offset。

- [x] **Step 3: 实现断线 supervisor**

若 rollout active，调用 `watchCodexRolloutTask`；若 rollout 尚未出现，按 200ms poll 等待 snapshot 重连、rollout active/terminal 或明确 release。只在真实 `task_complete/turn_aborted`、Desktop 明确 terminal event，或 release 后读取到 rollout terminal 时调用 `claimTerminal`。

- [x] **Step 4: 阻止错误路径提升 pending**

删除 `runExternalCodexTaskWatcher` 的无条件 defer finish。watch error 只有在权威 terminal failure 时发送失败；断线、owner unknown 和版本不兼容只更新卡片状态并保留 active task/pending。

- [x] **Step 5: 验证并提交**

Run: `gofmt -w messaging/codex_external_watch.go messaging/handler_codex_live_recovery_test.go messaging/handler_codex_live_progress_test.go messaging/codex_external_task.go messaging/codex_rollout_watch.go`

Run: `go test ./messaging -run 'TestCodexDesktop(Disconnect|Progress|Terminal|Reconnect)|TestCodexPendingWaits' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add messaging/codex_external_watch.go messaging/handler_codex_live_recovery_test.go messaging/handler_codex_live_progress_test.go messaging/codex_external_task.go messaging/codex_rollout_watch.go
git commit -m "断线后继续观察 Codex 本地任务"
```

## Task 12: 按实时 owner 路由普通消息、引导和停止

**Files:**
- Create: `messaging/handler_codex_live_message_control_test.go`
- Modify: `messaging/task_commands.go`
- Modify: `messaging/task_external_control.go`
- Modify: `messaging/codex_agent_task.go`

- [ ] **Step 1: 写消息与控制失败测试**

```go
func TestCodexDesktopIdleMessageStartsDesktopTurn(t *testing.T) {}
func TestCodexDesktopActiveMessageQueuesOnePending(t *testing.T) {}
func TestCodexDesktopDisconnectedRejectsStartGuideAndStop(t *testing.T) {}
func TestCodexDesktopGuideUsesCurrentTurn(t *testing.T) {}
func TestCodexDesktopStopWaitsForTerminalAndKeepsPending(t *testing.T) {}
func TestCodexPendingMessageRechecksOwnerBeforeAutorun(t *testing.T) {}
func TestUnauthorizedUserCannotGuideStopOrReadPendingAction(t *testing.T) {}
```

- [ ] **Step 2: 普通消息只走统一 Chat**

`startCodexAgentTask` 在登记自己的新任务前先执行只读 owner/state 解析：idle Desktop thread 的普通消息由 `ACPAgent.ChatWithProgress` owner 路由到 follower start；若发现 Desktop active 且 Handler 尚未登记 external task，先调用 `startExternalCodexTaskIfActive` 建立 watcher，再把当前消息保存为 pending。active 时只保留一条 pending，第二条拒绝；Messaging 不直接构造 IPC start payload。

- [ ] **Step 3: 控制前读取实时 binding**

`/guide` 和 `/stop` 不再相信 task 中缓存的 `externalControl`；先校验 actor，再读取 `CurrentCodexThreadBinding` 和 `ReadCodexThreadState`。只有 `desktop_live + matching active turn` 才允许 follower steer/interrupt。

- [ ] **Step 4: 分离 stop 和 cancel 语义**

`/stop` 成功后 phase 进入 stopping，保留 pending 并等待权威终态；`/cancel` 只撤回 pending。断线时 `/stop` 返回“实时连接已断开，无法确认停止”，不能回退到本地 context cancel 并伪装成功。

- [ ] **Step 5: 验证并提交**

Run: `gofmt -w messaging/handler_codex_live_message_control_test.go messaging/task_commands.go messaging/task_external_control.go messaging/codex_agent_task.go`

Run: `go test ./messaging -run 'TestCodexDesktop(Idle|Active|Disconnected|Guide|Stop)|TestCodexPendingMessageRechecks|TestUnauthorizedUser' -count=1 -timeout 60s`

Expected: PASS。

```bash
git add messaging/handler_codex_live_message_control_test.go messaging/task_commands.go messaging/task_external_control.go messaging/codex_agent_task.go
git commit -m "支持远程控制 Codex Desktop 当前任务"
```

## Task 13: 集成回归、真实 App 验收与交付审查

**Files:**
- Modify: `tasks/todo.md`
- Modify: `tasks/lessons.md`（仅当本轮真实验收发现可复用规则）
- Test: `agent/codex_desktop_*_test.go`
- Test: `messaging/handler_codex_live_*_test.go`

- [ ] **Step 1: 运行受影响包测试**

Run: `go test ./agent ./messaging -count=1 -timeout 60s`

Expected: PASS。

- [ ] **Step 2: 运行受影响包 race**

Run: `go test -race ./agent ./messaging -count=1 -timeout 120s`

Expected: PASS，无 owner registry、pending map、state projector 或 watcher race。

- [ ] **Step 3: 运行全仓验证**

Run: `go test ./... -count=1 -timeout 120s`

Run: `go test -race ./... -count=1 -timeout 180s`

Run: `go vet ./...`

Run: `staticcheck ./...`

Run: `python3 scripts/validate_docs.py . --profile generic`

Run: `git diff --check`

Expected: 全部 PASS；任何失败都保留完整输出并回到对应 Task 修复，不跳过、不降级。

- [ ] **Step 4: 执行真实 Codex App 手工验收**

1. 在 Codex App 打开一个包含至少两轮上下文的 thread，启动长任务。
2. 在飞书切换该 thread，确认立即显示相同模型、推理强度、任务和最新进度，日志中没有 WeClaw `thread/resume`。
3. 飞书发送 `/guide`，确认本地 App 当前 turn 接收引导；再发送 `/stop`，确认 App 中同一 turn 被中断。
4. Desktop idle 后从飞书发送上下文问题，确认 App 原 thread 出现新 turn，回答能引用切换前本地上下文。
5. 触发命令/文件审批和 requestUserInput，从飞书处理后确认本地任务继续；未授权账号不能查看或回复。
6. 任务运行中断开 IPC，确认不创建第二个 app-server turn、不自动执行 pending；rollout 继续回推终态。
7. 恢复 IPC，确认 snapshot 到达前 `/guide`、`/stop` 被拒绝，snapshot 到达后恢复控制。
8. 退出 Codex App并等待 rollout 终态，再发送消息，确认 WeClaw 刷新 ACP runtime 后恢复同一个 thread，而不是创建新 thread。

- [ ] **Step 5: Review gate**

逐项检查：单 owner 不变量、所有 mutating request 的不确定交付不重试、socket uid/mode、日志不含 prompt/审批正文、函数不超过 50 行、文件不超过 300 行、测试覆盖所有 Spec 验收项。

- [ ] **Step 6: 更新任务记录并提交**

`tasks/todo.md` 勾选 P4/P5，并记录自动验证和手工验收证据。若真实协议与 golden fixture 不一致，先更新 Spec 和本计划再修代码。

```bash
git add agent messaging tasks/todo.md tasks/lessons.md
git commit -m "完成 Codex Desktop 实时接管验收"
```

## 并行与写冲突安排

- Task 1、2、3 必须串行：frame、client、state projector 逐层依赖。
- Task 4 在 Task 3 后串行：它会修改共享 `codexTurnEvent` 和 collector。
- Task 5、6、7 必须由同一执行者串行：共享 `ACPAgent`、owner registry、process lifecycle 和 thread map。
- Task 8 可在 Task 5 完成后由独立执行者处理 Messaging interaction 文件，但合并前必须等待 Task 4 的 `UserInputHandler` 契约稳定。
- Task 9、10、11、12 必须由同一执行者串行：共享 route、active task、pending 和 external watcher 状态。
- Task 13 的 Agent、Messaging、race、静态检查可以并行运行；主流程统一汇总结果并执行 review gate。
- 任意两个执行者不得同时修改 `agent/acp_agent.go`、`agent/acp_threads.go`、`messaging/task_state.go` 或 `messaging/codex_external_task.go`。

## 计划完成标准

- 飞书和微信切换 Desktop-owned thread 时不会调用 WeClaw `thread/resume`。
- 下一条远程消息出现在 Codex App 同一 thread，并继承本地内存上下文。
- active turn 可远程查看、引导、停止、审批和回答结构化问题。
- IPC 断线不触发双执行、不误报终态、不提前执行 pending。
- Desktop 明确释放且 rollout 终态后，WeClaw 在无 active ACP turn 时刷新进程并恢复同一 thread。
- 自动化测试、race、vet、staticcheck、文档校验和真实 App 验收全部通过。
