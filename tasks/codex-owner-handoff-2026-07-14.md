# Codex 控制权显式移交

## 目标

- 为同一个 Codex thread 建立明确的本地与远程控制权，避免 Codex Desktop 和 WeClaw app-server 同时写入。
- 支持 `/cx owner`、`/cx owner remote`、`/cx owner desktop`，让用户主动决定由飞书/微信还是 Codex Desktop 继续任务。
- 保留当前会话、上下文、任务观察、排队和最终结果回推能力，不通过隐式新建会话规避冲突。
- 在探测超时、运行时冲突或刷新失败时显式失败，不产生假成功或静默回退。

## 非目标

- 不修改 Claude 会话与 ACP 行为。
- 不改变 `/stop`、`/cancel`、`/guide` 和暂存消息的既有语义。
- 不阻止用户启动 Codex Desktop；检测到违反远程控制权的本地写入时进入冲突态。
- 不把 `thread/unsubscribe` 当作立即卸载 thread 或释放写入权的机制。
- 不自动创建新 thread，也不在控制权未知时自动选择一方。

## 当前事实

- `agent/codex_desktop_owner.go::observeDesktopSnapshot` 在 owner 为 `weclaw_runtime` 时无条件忽略 Desktop 快照，无法区分“仍在执行的远程 turn”和“Desktop 已经开始了另一个 turn”。
- `agent/codex_desktop_owner.go::bind` 会直接复用 `desktop_live`、`weclaw_runtime`、`persisted_only` 缓存，函数名虽是重新绑定，但不保证每次重新探测。
- `messaging/codex_runtime_binding.go::resolveCodexRuntime` 同时承担会话选择、实际运行时判断和自动恢复，持久化意图与进程内事实没有分离。
- `agent/acp_chat.go::chat` 根据缓存的 `CodexThreadBinding.Owner` 选择 Desktop follower 或独立 app-server；最终探测与 `turn/start` 之间没有统一写入租约。
- `agent/codex_runtime_recovery.go::restartCodexAppServer` 会直接停止共享 app-server；其他 thread 的 app-server turn 可能被一并中断。
- `messaging/codex_sessions.go::codexSessionState` 只保存 route 到 workspace/thread 的选择，没有 thread 级控制权记录。
- `messaging/codex_rollout_task.go::readCodexRolloutEvents` 已按完整 JSONL 行推进偏移，可作为刷新前后的 rollout 新鲜度检查基础。
- `messaging/codex_session_command_dispatch.go::dispatchCodexUtilityCommand` 是新增 `/cx owner` 命令的现有分发入口。
- `messaging/codex_feishu_cards.go::handleFeishuCodexSessionCommand` 与 `platform.Replier.AskChoices` 已能复用为飞书控制权选择卡片。
- 官方 Codex app-server 文档说明 `thread/unsubscribe` 只取消当前连接的订阅；最后一个订阅者离开后 thread 仍会保持加载，默认空闲约 30 分钟才卸载，因此不能作为即时所有权移交屏障。

## 设计原则

1. 会话选择、用户控制意图、实际运行时三类状态必须分离。
2. 用户命令决定控制意图；Desktop 探测、rollout 检查和进程状态只验证能否安全执行该意图。
3. 实际运行时和写入租约只存在于当前进程，重启后必须重新探测。
4. 普通消息不能隐式取得控制权；无有效控制权时只提示选择。
5. 写入租约覆盖最终 Desktop 探测、新鲜度检查、事件通道注册、`turn/start` 接受和 turn 终态。
6. 共享 app-server 刷新必须先排空其他 turn；超时返回真实错误，不强制终止其他 thread。
7. Desktop 在远程控制期间开始不匹配的 turn 时进入 `conflict`，阻止后续远程写入并通知用户重新选择。

## 决策驱动因素

- 用户通常提前知道即将离开本机或返回本机，显式移交比后台猜测更符合真实操作意图。
- Desktop 与独立 app-server 没有跨进程原子写锁，单靠进程存在、socket 或历史快照不能证明唯一写入者。
- WeClaw 必须继续支持通过 Desktop follower 接管同一内存上下文；不能一律切到独立 app-server。
- app-server 是 Agent 级共享子进程，恢复单个 thread 时必须保护其他会话。
- 现有 route、任务队列和 rollout 观察链路已较成熟，应最小化无关改动。

## 方案对比

### 方案 A：仅自动探测并自动选择运行时

- 优点：用户操作少。
- 缺点：无法从“Desktop 存在”推导用户是否要本地继续；探测与开始 turn 之间仍有竞争窗口。
- 结论：淘汰。它不能从根因上消除双写，也会重复当前 owner 缓存误判。

### 方案 B：启动独立锁服务并要求 Desktop 配合

- 优点：理论上可以提供真正的跨进程互斥。
- 缺点：Codex Desktop 当前不消费 WeClaw 自定义锁协议；需要修改上游 Desktop，超出本项目控制范围。
- 结论：当前不可行。

### 方案 C：显式控制意图 + 进程内写入租约 + 新鲜度屏障

- 优点：用户意图明确；可继续复用 Desktop follower；能阻止 WeClaw 内部并发写，并在 Desktop 越权写入时显式进入冲突态。
- 缺点：无法物理禁止本地用户在 Desktop 点击发送，只能检测并停止远程侧继续写入。
- 结论：采用。它在不修改 Codex Desktop 的前提下提供最强、最可解释的一致性保证。

## 推荐方案

### 状态模型

- 会话选择：继续由 `codex-sessions.json` 中 route -> workspace/thread 绑定表示。
- 期望控制方：新增 thread 级 `unclaimed | desktop | remote`；`remote` 同时保存 route binding key、conversation ID 和单调 revision。
- 实际运行时：进程内使用 `unknown | desktop | weclaw | conflict`，同时记录运行代次、活动 turn、预期 Desktop turn 和 rollout checkpoint。
- 旧 `weclaw_runtime`、`persisted_only`、`desktop_disconnected` 只作为一次性迁移输入；重启后实际运行时统一为 `unknown`，不再持久化旧 owner 结论。

### 命令语义

- `/cx owner`：显示选中 thread、期望控制方、实际运行时、活动任务和冲突原因；飞书返回“远程接管 / 交还 Desktop”卡片。
- `/cx owner remote`：当前 route 显式取得远程控制。Desktop 在线时通过 follower 继续同一内存上下文；Desktop 不在线时排空并刷新 app-server，再恢复同一 thread。
- `/cx owner desktop`：停止接受新的远程消息并交还 Desktop。存在远程 active task 或暂存消息时拒绝，提示先等待、`/stop` 或 `/cancel`。
- 另一个远程 route 可以在原 route 空闲且无暂存消息时显式接管；存在任务时拒绝抢占。
- 探测结果未知、rollout 在屏障期间变化或 app-server 排空超时时，控制意图保持原值。

### 普通消息语义

- 当前 route 是 `remote` owner：进入统一运行时检查并执行。
- 期望 owner 是 `desktop`：只提示发送 `/cx owner remote`，不自动接管。
- 另一个 route 是 `remote` owner：拒绝并显示当前远程控制窗口。
- `unclaimed`：提示选择控制方；飞书展示选择卡片。
- 实际运行时为 `conflict`：阻止写入，要求显式重新选择控制方。
- Desktop active turn 在当前 route 取得远程控制后仍按现有外部任务机制观察；普通消息暂存并在终态后自动执行。

### 运行时安全

- Desktop follower 启动的远程 turn 记录预期 turn ID；同 ID 快照属于同一任务，其他 active turn 触发冲突。
- app-server 使用全局 drain gate：`running -> draining -> restarting -> running`。draining 后禁止新 turn，等待既有 turn 归零再重启。
- app-server 恢复前后比较 rollout 路径、完整行偏移、文件大小和最新 turn ID；`thread/read` 结果也必须与 checkpoint 一致。
- `RunCodexTurn` 在真正开始前再次核对控制 revision、route、运行代次和 Desktop 状态，消除“预检通过后状态变化”的窗口。
- `thread/unsubscribe` 仅保留其订阅语义，不参与控制权释放。

## 风险与预想失败场景

- Desktop 在远程租约期间手动发送：进入 `conflict`，尝试中断 WeClaw 正在启动或执行的 turn，停止后续远程消息，不自动合并。
- Desktop follower 在 `turn/start` 响应前先广播 active：暂存候选 turn，收到响应后按 turn ID 对齐；无法对齐则冲突。
- app-server 还有其他 thread 在执行：handoff 等待 drain；超时后不停止进程、不改变期望控制方。
- rollout 在刷新期间继续增长：判定 checkpoint 失效，重新探测；仍不稳定则失败。
- 进程重启：保留用户选择的期望控制方，但实际运行时回到 `unknown`，下一次命令或消息重新校验。
- 旧状态迁移：保留 route/thread 选择，不把旧运行时 owner 当作新控制意图；默认 `unclaimed`。
- 卡片重复点击或乱序：复用飞书 session metadata 与现有 dispatch order；控制 revision 保证过期动作不能覆盖新选择。

## 执行计划

- [x] P0 串行：完成现状分析、方案取舍、风险决策和本 Spec，等待 HARD-GATE。
- [x] P1 串行：在 `messaging/codex_sessions.go`、`messaging/codex_session_persistence.go` 新增 state v2 的 thread 控制意图；新增 `messaging/codex_control_store.go`，实现 `controlIntent(threadID string)` 与 `updateControlIntent(update codexControlIntentUpdate)` 的原子读写和 revision 校验。
- [x] P2 串行：重构 `agent/codex_live_runtime.go` 的公共类型与 `CodexLiveRuntimeAgent`；新增 `agent/codex_runtime_lease.go` 和 `agent/codex_runtime_probe.go`，实现 `InspectCodexRuntime(context.Context, CodexRuntimeRequest)`、`HandoffCodexRuntime(context.Context, CodexRuntimeRequest)`、`RunCodexTurn(context.Context, CodexTurnRequest)`。
- [x] P3 串行：在 `agent/codex_app_server_gate.go` 实现共享 app-server drain gate；修改 `agent/codex_runtime_recovery.go::restartCodexAppServer` 与 `agent/codex_app_server_turn.go::chatCodexAppServerTurn`，确保刷新和 turn 使用同一代次屏障。
- [x] P4 串行：修改 `agent/codex_desktop_owner.go::observeDesktopSnapshot`、`agent/codex_desktop_connector.go::handleBroadcast`、`agent/acp_chat.go::chat`，按预期 turn 识别正常 follower 事件与 Desktop 双写冲突。
- [x] P5 串行：修改 `agent/acp_types.go::acpPersistedState`、`agent/acp_state.go::loadState`、`agent/acp_state.go::snapshotPersistedState`，升级 state v3；旧 live binding 只迁移为未知运行时，不再持久化实际 owner。
- [x] P6 串行：新增 `messaging/codex_owner_command.go`；修改 `messaging/codex_session_command_dispatch.go::dispatchCodexUtilityCommand`、`messaging/codex_session_status.go::buildCodexSessionHelpText`，实现三个 owner 命令及状态文本。
- [x] P7 串行：修改 `messaging/codex_feishu_cards.go::handleFeishuCodexSessionCommand`，复用 `platform.Replier.AskChoices` 发送所有权卡片，并保留飞书 session metadata 与按钮顺序屏障。
- [x] P8 串行：修改 `messaging/codex_runtime_binding.go::resolveCodexRuntime`、`messaging/agent_conversation.go::prepareCodexConversation`、`messaging/codex_task_start.go::preflightCodexTaskStart`、`messaging/codex_agent_task.go::executeCodexAgentTurn`，让普通消息只按已持久化控制意图执行，并在 turn 内再次校验。
- [x] P9 串行：修改 `messaging/codex_session_switch.go::handleCodexSwitchForRouteWithOptions`、`messaging/codex_external_task.go::startExternalCodexTaskWatcher`、`messaging/codex_external_watch.go::superviseExternalCodexWatch` 与 `messaging/task_external_control.go`，分离“选择会话”和“取得控制权”，保持 active Desktop 任务观察与排队。
- [x] P10 可并行验证：补齐 Agent 状态机、存储迁移、命令卡片、跨 route 互斥、Desktop 双写、drain 超时和预检竞态测试；并行执行独立测试套件，最后串行整合 Review Gate。

## 写冲突与并行说明

- P1 至 P9 不使用 subagent 并行写代码。所有阶段都会修改 owner/runtime 契约或其调用方，存在共享类型、状态机和测试夹具写冲突，串行更安全。
- P10 可并行运行 `agent`、`messaging`、`feishu` 三组只读验证；主流程统一审阅失败与覆盖率，不让验证进程修改源码。
- 若实现中发现公共接口必须偏离本 Spec，先停止、更新 Spec 并重新确认，不边改计划边编码。

## 验证矩阵

| 场景 | 预期结果 | 主要测试位置 |
|---|---|---|
| 新安装或旧 state v2 升级 | thread 选择保留，控制方为 `unclaimed`，实际运行时为 `unknown` | `agent/codex_desktop_owner_test.go`、`messaging/codex_sessions_test.go` |
| `/cx owner` | 文本显示完整状态；飞书返回两个按钮且回放到原 route | `messaging/handler_codex_owner_command_test.go`、`messaging/handler_codex_owner_feishu_test.go` |
| Desktop 在线时 remote 接管 | 通过 follower 在同一 thread 执行，不启动独立 app-server turn | `agent/codex_runtime_lease_test.go` |
| Desktop 离线时 remote 接管 | drain 成功、重启 app-server、checkpoint 一致后恢复同一 thread | `agent/codex_app_server_gate_test.go`、`messaging/handler_codex_owner_command_test.go` |
| 交还 Desktop 时有任务或暂存 | 拒绝交还，原控制意图不变 | `messaging/handler_codex_owner_command_test.go` |
| 非 owner route 发送普通消息 | 拒绝，不调用 Agent | `messaging/handler_codex_owner_message_test.go` |
| 预检后控制 revision 改变 | `RunCodexTurn` 拒绝，不发送 `turn/start` | `agent/codex_runtime_lease_test.go` |
| Desktop 产生不匹配 active turn | 进入 `conflict`，远程写入被阻止 | `agent/codex_runtime_lease_test.go` |
| drain 等待其他 thread 超时 | app-server 不被停止，返回显式错误 | `agent/codex_app_server_gate_test.go` |
| active Desktop turn 后排队 | 观察进度和终态，随后自动执行暂存消息 | `messaging/handler_codex_owner_message_test.go` |

验证命令：

```bash
gofmt -w agent messaging feishu
go test ./agent -count=1 -timeout 60s
go test ./messaging -count=1 -timeout 60s
go test ./feishu -count=1 -timeout 60s
go test -race ./agent -count=1 -timeout 60s
go test -race ./messaging -count=1 -timeout 60s
go test -race ./feishu -count=1 -timeout 60s
go test ./... -count=1 -timeout 120s
go vet ./...
staticcheck ./...
go build ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

核心新增函数使用 `go test -coverprofile` 与 `go tool cover -func` 复核，目标覆盖率不低于 80%。

## 进度记录

- 2026-07-14：完成只读分析、官方协议核对、三段 Spec 确认与最终文件级计划。
- 2026-07-14：P1 完成；控制意图持久化、revision 校验、远程 route 校验和并发单赢家测试通过。
- 2026-07-14：P2 至 P9 完成；Agent 与消息层全包测试通过，进入 P10 统一验收。
- 2026-07-14：P10 完成；Review Gate 补充 thread 级控制锁、冲突态粘滞保护、工作空间权限检查和回滚错误反馈。
- 当前状态：HARD-GATE 已通过，P0 至 P10 全部完成。

## 验证结果

- 规划阶段仅确认 `main` 与 `origin/main` 一致，当前基线为 `v0.1.175`。
- `go test ./... -count=1 -timeout 120s` 通过。
- `go test -race ./agent ./messaging ./feishu -count=1 -timeout 120s` 通过。
- `go vet ./...`、`staticcheck ./...`、`go build ./...` 通过。
- `python3 scripts/validate_docs.py . --profile generic`、`git diff --check` 通过。
- 覆盖率：`agent` 80.0%，`messaging` 81.0%。

## Review 小结

- 终态：finished。
- Spec 符合度：通过；会话选择、持久化控制意图和实际运行时已经分离，普通消息不再隐式取得控制权。
- 安全检查：通过；控制命令继续遵守普通用户工作空间限制，未新增密钥或未校验的解释器输入。
- 测试与验证：通过；状态迁移、CAS、双写冲突、drain、跨窗口任务准入和飞书卡片均有自动化覆盖。
- 复杂度检查：通过；新增生产文件均低于 300 行，新增核心函数按职责拆分。
- Document-refresh: not-needed
- 原因：本轮已更新内置命令帮助与执行状态文档，用户未要求扩展产品说明文档。
- 剩余风险：Codex Desktop 不消费 WeClaw 的锁协议，本地越权写入只能被检测并转为冲突态，无法从进程外物理禁止。
- 潜在技术债：thread 控制记录目前随状态长期保留，尚未提供独立的历史记录清理策略。
- 结论：通过。

## HARD-GATE

用户明确确认本计划前，不进行任何业务代码、测试代码或运行时配置修改。
