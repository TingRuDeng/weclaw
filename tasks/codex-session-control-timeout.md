# Codex 会话控制命令永久阻塞修复计划

## 目标

- 修复 `/cx switch` 卡住后，同窗口的普通消息和 `/cx owner remote` 永久等待的问题。
- 保留同一窗口会话切换、普通任务和控制权移交之间的顺序一致性。
- 将锁等待、运行位置探测和控制权移交变成可取消、有限时、可观测的操作。
- 为真实故障序列补充确定性并发回归测试。

## 非目标

- 不改变 `/cx switch`“先提交选择、探测失败不回滚”的既有语义。
- 不自动选择 remote 或 desktop 控制方。
- 不用重启、静默重试或自动新建会话掩盖失败。
- 不在本轮处理 `remoteControl/status/changed` 未消费事件；当前没有证据证明它是直接根因。
- 不调整 Codex Desktop IPC 的 1.5 秒发现超时、10 秒请求超时和 10 秒状态投影超时。

## 当前事实

- 运行二进制是 `v0.1.176`、提交 `946ca1f`；当前 `main` 为 `f909889`。两者在 `agent/`、`messaging/` 上无差异，升级当前 `main` 不能直接修复本问题。
- `2026-07-15 06:09:49` 与 `10:56:09` 两次切换同一 thread 后均未出现完成证据；第二次已把选中 thread 持久化。
- `/cx owner remote` 只有收到消息日志，没有进入 app-server 重启或 `thread/resume` 的后续证据。
- 运行时 `/api/runtime` 返回 `active_tasks=0`，不是正常任务长期占用。
- `messaging/codex_session_command.go:handleCodexSessionCommandForRouteResult` 在整个命令分发期间持有 binding lock。
- `messaging/codex_session_switch.go:handleCodexSwitchForRouteWithOptions` 在提交选择后继续获取 thread control lock，并在持锁状态探测 runtime。
- `messaging/execution_lock.go:lockAgentExecution` 使用不可取消、无超时的 `sync.Mutex.Lock()`。
- `agent/acp_rpc.go:callWithSequence`、Desktop 请求和 app-server gate 已遵守传入 context；缺口在于消息入口没有控制命令总时限，执行锁也不能响应 context。

## 设计原则

- 根因修复：消除永久等待，不把重启当作正常恢复机制。
- 保持顺序：binding 状态提交完成前，后续普通任务仍不得越过切换屏障。
- 状态诚实：超时后区分“选择已保留”“控制权结果未确认”，禁止误报成功或回滚已提交选择。
- 最小影响：不重构 owner 状态机，不改变 Desktop/ACP 协议，只补齐控制面 context 与锁能力。
- 可测试：超时参数通过 `Handler` 默认策略注入，测试使用毫秒级时限，不依赖真实等待。

## 决策日志

### 方案 A：仅升级或重启服务

- 优点：操作成本低。
- 缺点：当前 `main` 的相关实现与部署版本一致；重启只能清除内存锁，同一目标已重复复现。
- 决策：淘汰，不能解决根因。

### 方案 B：只给 Desktop/ACP 请求增加超时

- 优点：改动较小。
- 缺点：Desktop 请求已经有超时；等待 `sync.Mutex` 的 goroutine 仍无法响应 context，后续 owner 命令仍可能永久等待。
- 决策：淘汰，覆盖不完整。

### 方案 C：移除或缩小 binding lock

- 优点：外部探测不会长期占用窗口 binding。
- 缺点：会破坏仓库已经建立的会话切换顺序屏障；普通消息可能在 runtime/owner 尚未确认时越过切换，重新引入路由错乱。
- 决策：本轮不采用；需要额外 generation/CAS 设计，修改成本和状态风险过高。

### 方案 D：上下文感知执行锁 + 控制命令总时限

- 优点：保留现有顺序和锁序，同时保证持锁操作与排队操作都有终止条件；影响集中、可确定性测试。
- 缺点：需要谨慎处理锁使用者计数、取消等待者回收，以及 owner 超时后的“结果未确认”语义。
- 决策：推荐。

## 推荐方案

1. 在 `messaging/execution_lock.go` 将内部不可取消互斥等待改为单令牌锁，并新增：
   - `lockAgentExecutionContext(ctx context.Context, key string) (func(), error)`
   - `lockCodexThreadControlContext(ctx context.Context, threadID string) (func(), error)`
   - 保留现有 `lockAgentExecution`、`lockCodexThreadControl` 作为 `context.Background()` 兼容包装，避免扩大调用面。
2. 在 `Handler` 中加入可测试的 Codex 控制命令策略：
   - 默认总时限 `90s`，覆盖 Desktop 探测、app-server 30 秒初始化和 thread 恢复。
   - 默认锁等待时限 `5s`，避免 owner 命令长时间无反馈地排队。
   - 两个值使用具名常量，测试可在 Handler 内覆盖为毫秒级。
3. 在 `messaging/codex_session_command.go:handleCodexSessionCommandForRouteResult` 为 `/cx` 控制面命令创建总 deadline，并确保 cancel 在返回时执行。
4. 在 `messaging/codex_session_command_dispatch.go:prepareCodexSessionCommand` 使用 context-aware binding lock：
   - 锁等待超时返回“前一项会话操作仍在处理，本次命令未执行”。
   - 不修改当前选择，不进入 owner handoff。
5. 在 `messaging/codex_session_switch.go:handleCodexSwitchForRouteWithOptions` 使用 context-aware thread control lock，并明确渲染：
   - 选择提交前超时：切换未发生。
   - 选择提交后探测超时：会话选择已保留，运行位置暂未确认。
6. 在 `messaging/codex_owner_command.go:handoffCodexOwner` 使用 context-aware thread control lock，并明确渲染：
   - 锁等待超时：移交未执行。
   - handoff 调用超时：移交结果未确认，持久化控制意图不提交；用户可重试查询。
7. 仅在超时/取消时记录结构化日志，字段包含命令、binding/thread、阶段和 elapsed；不记录消息正文或凭据。

## 风险与预想失败场景

- 取消等待者错误回收 map entry，导致仍在使用的锁被替换：必须用 `users` 引用计数覆盖 holder、waiter 和取消路径。
- unlock 重复发送令牌导致并发进入：unlock 必须一次性执行，测试覆盖重复释放防护。
- `/cx switch` 在选择已持久化后超时却被误报为切换失败：继续沿用“选择保留、探测失败单独展示”的既有契约。
- owner handoff 后端已经产生部分效果但响应超时：不得宣告“控制方未变化”，只能报告“结果未确认”；持久化意图保持旧 revision，下一次查询重新探测。
- 统一 90 秒 deadline 影响 `/cx quota`、`/cx model` 等命令：这些属于控制面操作，也应有限时；现有正常路径远低于 90 秒。
- context 未被某个下游调用遵守：回归测试先覆盖当前故障的 fake inspect/handoff；若仍出现超时后不释放，停止实施并回到具体阻塞函数补证据。

## 执行计划

- [x] P1 串行：在 `messaging/execution_lock_test.go` 先补取消等待者、锁复用和引用回收失败测试。
- [x] P2 串行：在 `messaging/execution_lock.go` 实现 context-aware keyed lock，保持现有 blocking API 兼容。
- [x] P3 串行：在 `messaging/handler.go`、`messaging/handler_constructor.go` 增加具名默认时限与测试注入字段。
- [x] P4 串行：在 `messaging/handler_codex_binding_race_test.go` 补“switch inspect 阻塞后 owner 不永久等待、switch 超时后锁可复用”的失败测试。
- [x] P5 串行：在 `messaging/codex_session_command.go`、`messaging/codex_session_command_dispatch.go` 接入总 deadline 和 binding 锁等待时限。
- [x] P6 串行：在 `messaging/handler_codex_live_switch_test.go` 补“探测超时仍保留选择”的回归，在 `messaging/codex_session_switch.go` 接入 thread 锁和超时文案。
- [x] P7 串行：扩展 `messaging/codex_live_fakes_test.go` 的 handoff 阻塞钩子；在 `messaging/codex_owner_command_test.go` 补“handoff 超时不提交 intent、锁可再次获取”的回归，并修改 `messaging/codex_owner_command.go`。
- [x] P8 串行：补超时阶段日志，检查函数长度、嵌套和锁序。
- [x] P9 串行：执行定向测试、`messaging` 包测试、全仓测试、race、vet、构建和文档门禁。
- [x] P10 串行：使用 `review-gate` 完成交付前审查，回填验证与剩余风险。

## 并行评估

- 不使用 subagent。
- 原因：生产改动集中在同一条 binding/thread 锁链，测试共享 `fakeCodexLiveAgent`；并行写入会产生同文件冲突并增加锁序遗漏风险。

## 验证矩阵

| 行为 | 测试位置 | 验收标准 |
|---|---|---|
| context 取消锁等待 | `messaging/execution_lock_test.go` | 等待者按 deadline 返回，holder 不受影响 |
| 取消等待者回收 | `messaging/execution_lock_test.go` | holder 释放后锁可再次获取，`taskLocks` 不泄漏 |
| switch runtime 探测阻塞 | `messaging/handler_codex_binding_race_test.go` | owner 有明确反馈，不永久阻塞；switch 最终释放 binding |
| switch 提交后超时 | `messaging/handler_codex_live_switch_test.go` | thread/workspace 选择保留，回复明确说明探测超时 |
| owner handoff 超时 | `messaging/codex_owner_command_test.go` | 不提交新 intent，不误报成功，thread lock 可复用 |
| 正常 switch/owner | 现有 live switch/owner 测试 | 行为与文案无非预期回归 |
| 锁顺序 | `messaging/handler_codex_binding_race_test.go` | 普通任务仍不能越过未完成的 binding 变更 |

验证命令：

```bash
go test ./messaging -run 'TestExecutionLock|TestCodexSwitch.*Timeout|TestCodexOwner.*Timeout|TestCodexSessionCommand' -count=1 -timeout 60s
go test ./messaging -count=1 -timeout 60s
go test ./... -count=1 -timeout 120s
go test -race ./messaging -count=1 -timeout 120s
go vet ./...
go build ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## 回滚策略

- 本改动不迁移持久化数据，也不改变 session 文件结构。
- 若验证失败，按阶段回退 context-aware lock 与命令 deadline 接入；不得回退为静默重试或自动重启。

## HARD-GATE

用户确认本计划前，不进行业务代码或测试代码修改。

## 进度记录

- 2026-07-15：完成运行日志、部署版本、持久化会话、runtime 状态和锁链只读诊断。
- 2026-07-15：完成方案比较并推荐“上下文感知执行锁 + 控制命令总时限”。
- 2026-07-15：用户显式确认计划，通过 HARD-GATE，进入串行执行阶段。
- 2026-07-15：完成 context-aware keyed lock、控制命令时限、switch/owner 超时语义和确定性回归测试。
- 2026-07-15：全包测试暴露外部任务观察被命令 cancel 误伤，已分离控制命令 context 与外部任务 context。

## 验证结果

- 定向超时与锁回归通过：`go test ./messaging -run 'TestExecutionLock|TestCodexSwitch.*Timeout|TestCodexOwner.*Timeout|TestCodexSessionCommand' -count=1 -timeout 60s`。
- `messaging` 包测试通过：`go test ./messaging -count=1 -timeout 60s`。
- 全仓测试通过：`go test ./... -count=1 -timeout 120s`。
- `messaging` race 通过：`go test -race ./messaging -count=1 -timeout 120s`。
- 静态检查与构建通过：`go vet ./...`、`go build ./...`。
- 文档与差异门禁通过：`python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

## Review 小结

- Spec 符合性：通过；未改变 switch 选择提交语义、owner CAS 语义或 Desktop/ACP 协议时限。
- 安全与可观测性：通过；仅记录命令、binding/thread、阶段、耗时和错误，不记录消息正文或凭据。
- 并发与状态一致性：通过；取消等待者参与引用计数回收，unlock 具备幂等保护，binding/thread 锁序保持不变。
- 测试审查：覆盖锁取消回收、switch 探测/锁等待超时、owner 查询/handoff/锁等待超时及真实故障序列。
- 剩余运行态风险：当前部署仍是旧二进制，完成发版并通过 `weclaw update` 前，线上行为不会改变。
- 非目标技术债：`remoteControl/status/changed` 未消费事件仍未处理，本轮没有证据证明其为直接根因。
