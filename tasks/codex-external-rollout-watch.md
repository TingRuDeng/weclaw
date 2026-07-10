# Codex App 外部任务镜像修复

## 目标与非目标

目标：飞书切换到 Codex App 本地运行中的会话后，立即返回当前任务与最新进度，持续镜像后续进度，并在本地任务结束后回传最终结果。

非目标：不让 WeClaw 接管 Codex App 进程；不为跨进程任务承诺 `/guide`、`/stop` 或审批控制；不改变 WeClaw 自身发起任务的 app-server 通知链路。

## 当前事实

- `messaging/codex_session_switch.go` 的 `handleCodexSwitchForRouteWithOptions` 已调用 `startExternalCodexTaskIfActive`。
- `messaging/codex_external_task.go` 当前只通过 WeClaw 自己的 `CodexThreadRuntimeAgent` 读取 active 状态并监听事件。
- 2026-07-10 19:52 的日志显示飞书卡片点击、`/cx switch` 和 `thread/resume` 均成功，但没有启动外部 watcher。
- 同一 thread 的 rollout 显示本地 turn 从 11:21 到 11:57 持续运行，11:52 切换时真实状态为 active。
- Codex App 和 WeClaw 分别运行独立 app-server 进程，且本机没有可复用的 daemon control socket；active 状态和通知流不跨进程共享。
- rollout 使用 `task_started`、`task_complete` 和 `turn_aborted` 提供明确的 turn 生命周期，并在 `task_complete.last_agent_message` 中保存最终结果。

## 设计原则

- 从共享的权威数据源修复跨进程状态，不伪造 app-server active 状态。
- 初次读取只做一次顺序扫描，后续从文件偏移量增量读取，避免周期性重扫大型 rollout。
- 只解析任务生命周期、用户任务、可展示进度和最终结果；忽略 reasoning 密文、token 统计和工具原始结果。
- rollout 读取或解析失败必须显式返回错误，不静默假成功。

## 方案对比

### 方案 A：连接 Codex App 的 app-server

优点：直接获得结构化状态和实时通知。

淘汰原因：Codex App 的 app-server 由 Electron 通过 stdio 持有，本机没有 daemon control socket，WeClaw 无法安全复用现有连接。

### 方案 B：跟踪共享 rollout 文件

优点：Codex App 和 WeClaw 已共享 `~/.codex/sessions`；生命周期和最终结果字段明确；无需改 Codex App 启动方式。

缺点：需要维护最小 JSONL 状态机，并区分可展示字段与内部记录。

结论：采用方案 B，这是当前唯一能同时恢复运行态、进度和最终结果的可验证方案。

### 方案 C：继续轮询 WeClaw 的 `thread/read`

淘汰原因：该调用只能看到 WeClaw app-server 的进程内状态，已被真实日志证明会把另一个 Codex App 进程的 active turn 判断为空闲。

## 推荐方案

新增 rollout task source：切换会话时先保留现有 app-server active 探测；若其返回非 active，再定位该 thread 的本地 rollout。若 rollout 最新生命周期为 `task_started` 且尚无同 turn 的终态，则登记只读外部任务镜像。

初始通知包含“任务”和“当前进展”。watcher 从初次扫描结束偏移继续读取追加行，将 `agent_message` 的 commentary 和可展示 reasoning 摘要映射为进度；收到同 turn 的 `task_complete` 后返回 `last_agent_message`，收到 `turn_aborted` 后明确结束为中断。

## 风险与失败场景

- 超大 rollout：初次顺序扫描时间与文件大小线性相关，但不把全文载入内存；后续只读增量。
- 半行写入：只有读到完整换行后才推进偏移，避免解析未写完 JSON。
- 同一 thread 开始新 turn：watcher只跟踪切换时确认的 turn ID，不能把下一轮结果误归属当前飞书任务。
- 文件删除、截断或无法读取：停止 watcher并向飞书返回明确错误。
- 跨进程控制：只展示和回传，不提示 `/guide`、`/stop` 可用。

## 执行计划

1. 新增 `messaging/codex_rollout_task.go`：实现 rollout 路径定位、初始状态扫描和最小事件解析。
2. 新增 `messaging/codex_rollout_watch.go`：实现基于文件偏移的增量 watcher，处理进度、完成、中断和上下文取消。
3. 修改 `messaging/codex_external_task.go`：抽象外部任务数据源，优先保留 app-server 路径，非 active 时启用 rollout 路径，并区分只读通知文案。
4. 必要时修改 `messaging/task_state.go` 与 `messaging/task_commands.go`：标记只读外部任务，避免提示不可用的 `/guide`、`/stop`。
5. 新增 `messaging/codex_rollout_task_test.go` 和 `messaging/handler_codex_rollout_task_test.go`，不扩充接近 300 行的现有测试文件。

## 验证矩阵

- rollout 最新 turn 正在运行：识别 active turn、任务文本和最新进度。
- rollout 最新 turn 已完成：不登记 watcher。
- 增量追加进度：只回传新进度，不重复历史内容。
- 增量追加 `task_complete`：回传 `last_agent_message` 并清理 active task。
- 增量追加 `turn_aborted`：明确返回中断，不挂死 watcher。
- 飞书卡片切换：立即收到任务与进度，完成后收到最终结果。
- WeClaw 自身 app-server active：继续使用原通知 watcher，不受 rollout 路径影响。
- 验证命令：`go test ./messaging -count=1 -timeout 60s`、`go test -race ./messaging -count=1 -timeout 60s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、`git diff --check`。

## HARD-GATE

用户已于 2026-07-10 确认，进入执行阶段。

## 进度记录

- [x] RED：飞书切换到 app-server 误判 idle、rollout 实际 active 的 thread 时，测试在“未登记外部任务镜像”处失败。
- [x] GREEN：新增 rollout 状态解析与增量 watcher。
- [x] GREEN：飞书切换后立即展示任务和进度，任务完成后返回最终结果。
- [x] 边界：只读镜像不暂存引导消息，`/stop` 不会假停止 Codex App 本地任务。
- [x] 验收：race、全仓测试、vet、文档契约和 review gate。
