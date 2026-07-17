# Codex 远程会话“选择即接管”设计

## 1. 背景

WeClaw 当前把 Codex 会话选择与控制权移交拆成两个操作：

- `messaging/codex_session_switch.go` 的 `handleCodexSwitchForRouteWithOptions` 保存 workspace/thread 选择并探测运行位置。
- `messaging/codex_owner_command.go` 的 `handoffCodexOwner` 通过 `/cx owner remote` 或 `/cx owner desktop` 单独移交控制权。

这种两阶段产品语义会产生“会话已经选择，但控制方仍未认领”的中间状态。当前故障进一步暴露了状态源冲突：`messaging/agent_conversation.go` 的 `syncCodexThreadFromAgent` 会把 ACP 中保存的旧 thread 回写到 session store，覆盖用户刚通过 `/cx switch` 完成的显式选择，导致后续接管错误 thread。

本设计把远程窗口的会话选择改为一次用户可见的所有权事务，消除选择、控制意图和实际运行位置之间的半成功状态。

## 2. 目标

- 飞书或微信远程窗口明确选择 Codex thread 时，自动取得该 thread 的远程控制权。
- 从会话 A 切换到会话 B 时，在同一事务内释放 A 并接管 B。
- 目标 Desktop 会话正在执行任务时，保持 Desktop 运行时不变，立即开始向当前远程窗口回传进度和结果。
- 接管活动 Desktop 任务后，当前远程窗口可以使用 `/guide` 和 `/stop`。
- 目标 thread 已由其他远程窗口控制时拒绝接管，原窗口必须先释放。
- 状态持久化、跨远程窗口 owner 或活动远程任务冲突时保持硬门禁；Desktop 探测不确定时由用户的显式 remote 选择恢复 WeClaw runtime。
- 所有权跨 WeClaw 重启持久保留，不设置空闲过期时间。
- 飞书、微信以及全部会话选择入口使用同一套消息层状态机。

## 3. 非目标

- 不允许远程窗口静默抢占另一个远程窗口。
- 不把 Desktop 断线或探测超时当作自动释放证据；用户显式选择 remote 时允许据此恢复 WeClaw app-server。
- 不在平台 adapter 中分别实现飞书和微信所有权逻辑。
- 不改变 Codex Desktop IPC framing、动作协议或公开网络边界。
- 不支持同一远程窗口同时拥有多个 Codex thread。
- 不让普通消息在用户明确释放后自动重新接管。
- 不为 `/cx new` 创建失败后的外部 Codex thread 提供删除能力；Codex 当前没有可靠的回滚删除契约。

## 4. 已确认的产品规则

### 4.1 选择与接管

以下入口只要最终选中了真实 thread，就必须执行同一套“选择并接管”事务：

- `/cx switch <thread>`
- `/cx <编号>`
- 飞书会话选择卡片按钮
- `/cx cd` 在目标 workspace 只有一个可选会话时的自动选择
- `/cx new` 创建出的新会话
- 当前默认 Agent 为 Codex 时的全局 `/new`
- `/cx owner remote` 兼容入口

`/cx ls`、`/cx pwd`、`/cx status` 等只读命令不改变选择或控制权。

### 4.2 单窗口单所有权

每个远程窗口同时最多拥有一个 Codex thread。当前窗口已拥有 A，再选择 B 时：

- A 的控制意图改为 `desktop`。
- B 的控制意图改为 `remote`，并绑定当前窗口的 route 和 conversation。
- 当前选择改为 B。
- 如果事务失败，上述三项都恢复原状态。

兼容历史状态时，如果同一路由已经拥有多个 thread，下一次所有权变更会把所有非目标 thread 纳入释放集合。任一旧 thread 仍有活动远程任务时，本次切换拒绝执行。

### 4.3 跨窗口冲突

目标 B 已由其他远程窗口控制时直接拒绝。错误回复只说明“其他远程窗口正在控制”，不暴露对方用户、route、conversation 或平台身份。

### 4.4 活动任务

- 当前 A 存在活动远程任务时禁止切走，用户必须等待完成或先执行 `/stop`。
- 目标 B 存在活动 Desktop 任务时允许接管。
- 接管 B 不迁移该任务，不调用 WeClaw app-server 的 `UseCodexThread`。
- B 继续由 Codex Desktop 执行；当前远程窗口获得控制权、观察权以及 `/guide`、`/stop` 操作权。

### 4.5 显式释放

`/cx owner desktop` 释放当前 thread 的远程控制权，但保留它作为最近选择项。释放后：

- 普通消息拒绝执行，不自动重新接管。
- 用户必须再次显式选择该 thread，或使用兼容入口 `/cx owner remote`。
- 所有权不会因空闲、时间经过或 WeClaw 重启而自动变化。

## 5. 状态模型

控制意图与实际运行位置保持两个独立维度：

- 控制意图：`unclaimed`、`desktop`、`remote(route)`。
- 运行位置：`desktop`、`weclaw`、`unknown`、`conflict`。

控制意图是写入授权的唯一事实源；实际运行位置只回答当前从哪里执行以及运行通道是否可用。普通消息不得用 Desktop 探测结果覆盖、释放或重新询问已经持久化的控制意图。

`desktop` 控制意图表示远程窗口无权写入，不要求 Codex Desktop 当前一定加载该 thread。`remote` 控制意图表示唯一获准的消息窗口；实际 turn 仍可运行在 Desktop。

核心不变量：

1. 一个 thread 同时最多有一个远程控制窗口。
2. 一个远程窗口同时最多控制一个 thread。
3. 成功选择的目标 thread 必须同时持久化为当前窗口所有。
4. 从 A 切换到 B 成功后，A 不再归当前窗口所有。
5. `remote` 控制意图必须同时包含 route binding key 和 conversation ID。
6. 所有权 revision 只允许通过 CAS 递增，不能覆盖并发获胜者。
7. WeClaw 同一 thread 同时最多持有一个 writer lease；Desktop 的独立 turn 可以并存，不据此锁死整个会话。

`unclaimed` 仅用于旧状态、空状态和迁移兼容；新的成功选择不会产生未认领目标。

## 6. 组件与职责

### 6.1 事务编排

新增 `messaging/codex_session_acquire.go`，承载统一的选择与接管事务。该文件负责：

- 读取原选择和所有相关控制意图快照。
- 计算当前 route 需要释放的 thread 集合。
- 在调用方已持有 binding 锁的前提下获取 thread 锁。
- 校验 workspace、活动任务和跨窗口冲突。
- 调用 Agent 层探测、接管和释放运行时。
- 调用 store 层一次性提交选择与控制意图。
- 在目标活动时启动外部任务观察。
- 失败时执行逆序补偿。

`/cx` 会话命令由 `messaging/codex_session_command_dispatch.go` 的
`prepareCodexSessionCommand` 在外层持有 binding 锁；全局 `/new` 由
`messaging/default_session.go` 持有同一把 binding 锁。事务入口必须明确要求调用方已持有
binding 锁，只在内部按 thread ID 排序获取一个或多个 thread 锁，避免重复加锁导致自锁。

`messaging/codex_session_switch.go` 只保留目标解析、入口适配和结果渲染，不再提前提交选择。

### 6.2 状态存储

在 `messaging/codex_control_store.go` 附近增加原子提交能力，使用 options 结构传入：

- binding key、workspace 和目标 thread。
- 所有被释放 thread 的旧 intent/revision。
- 目标 thread 的旧 intent/revision。
- 新的 remote route 和 conversation。

提交方法必须：

1. 持有 `saveMu`，防止并发快照交错。
2. 在 `mu` 内重新校验全部 expected revision。
3. 同时更新选择、活动 workspace 和全部控制意图。
4. 只调用一次 `persistStateLocked`。
5. 文件写入失败时恢复完整内存快照。

状态仍保存在同一个 `codex-sessions.json` 文件中；现有字段已经足够表达新规则，不新增第二个所有权文件。

### 6.3 Agent 运行时

`agent/codex_runtime_probe.go` 继续作为显式接管和运行通道恢复的探测边界：

- Desktop live：保持 Desktop runtime，只更新远程控制意图。
- Desktop 已明确释放且 rollout 稳定：允许恢复到 WeClaw app-server。
- Desktop 所有权未知或旧 conflict 尚未清除：普通消息不猜测；用户显式选择 remote 时校验 rollout 并恢复 WeClaw app-server。
- writer lease 存在：失败，不中途移交。
- Desktop 与 WeClaw 出现不同 turn 时允许并存；Desktop 快照不得覆盖或取消 WeClaw 正在执行的 lease，确认的新 Desktop 快照可以清除旧 runtime conflict。
- 普通消息只读取接管事务已经建立的 runtime binding，不调用 `InspectCodexRuntime`。
- runtime 为 `unknown`、断线或超时时，普通消息拒绝本次写入但保留 remote owner，不触发 Handoff，也不返回 owner 选择卡片。
- `InspectCodexRuntime` 只用于显式选择/接管、状态刷新以及 Desktop 重连或冲突恢复。

### 6.4 外部任务观察

`messaging/codex_external_task.go` 继续复用现有 active task 生命周期。选择事务只在以下任一条件成立后报告成功：

- 目标确认空闲或已经终态。
- 目标活动任务的观察已经启动，并绑定当前 route/thread/turn。

活动任务结束后，观察器继续负责进度、最终结果和暂存消息处理；所有权不会因任务结束自动释放。

### 6.5 ACP 状态回填

`messaging/agent_conversation.go` 的 `syncCodexThreadFromAgent` 仅在 session store 对应 workspace 没有 thread 且不处于 pending new 时，允许从 ACP 状态回填。显式选择一旦存在，ACP 旧映射不得覆盖它。

## 7. 事务流程

### 7.1 正常切换 A 到 B

1. 读取当前选择、当前 route 拥有的所有 thread、A/B intents 和 revisions。
2. 按 thread ID 排序后获取锁；binding 锁始终先于 thread 锁，全部锁保持到成功结束或补偿完成。
3. 重新读取并校验快照，防止锁等待期间状态变化。
4. 校验旧 thread 没有活动远程任务。
5. 校验 B 没有被其他远程窗口控制。
6. 探测 B 的实际 runtime 和活动状态。
7. 以 proposed revision 让当前 route 取得 B。
8. 释放当前 route 持有的所有非目标 thread。
9. B 活动时先预留当前 conversation 的观察槽，不启动第二个 watcher。
10. 一次性持久化目标选择、B remote intent 和旧 thread desktop intents。
11. 激活已预留的观察器；B 空闲时跳过该步。
12. 只有全部完成后返回“已切换并接管”。

### 7.2 幂等选择

B 已由当前 route 控制且已经是当前选择时：

- ready runtime 直接复用；`unknown/conflict` runtime 按本次显式选择重新恢复。
- 把遗留的同 route 多所有权纳入本次释放集合。
- 活动时恢复或复用观察器。
- 不重复递增无意义 revision，不重复发送最终结果。

### 7.3 新建会话

`/cx new` 与当前默认 Agent 为 Codex 时的全局 `/new` 先通过 Agent 创建 thread，再把新
thread 作为 B 进入同一接管事务。如果接管失败：

- 原选择和原所有权恢复。
- 新 thread 不设为当前选择，不写入 remote intent。
- 新 thread 可能继续存在于 Codex 历史中；回复必须明确创建后的接管失败，不能宣告完整成功。

## 8. 错误处理与补偿

### 8.1 失败分类

- 校验失败：不调用运行时，不修改状态。
- B 的 Desktop 探测失败或超时：显式 remote 选择在 checkpoint 稳定时恢复 WeClaw app-server。
- B 接管失败：恢复 B 原 intent，A 不变。
- B 已接管但旧 thread 释放失败：逆序恢复已释放 thread，再恢复 B。
- CAS 冲突：不覆盖并发获胜状态，补偿本次运行时变更。
- 文件持久化失败：内存和磁盘保持旧快照，补偿运行时。
- 活动任务观察启动失败：本次切换不报告成功，通过反向 store CAS 和运行时移交执行完整补偿。

### 8.2 不确定结果

超时后不得重发可能产生副作用的 IPC 请求。Desktop 探测超时本身不再等同于写入冲突；普通消息保持只读现有 binding，用户显式选择 remote 时改走 WeClaw runtime 恢复。只有运行时副作用、控制 revision 或补偿结果本身无法确认时：

- thread 标记为冲突或所有权未知。
- 新任务、`/guide` 和 `/stop` 均拒绝执行。
- 用户收到“移交结果未确认”的明确回复。
- 后续 `/cx owner`、`/cx status` 可以重新探测；重新选择或 `/cx owner remote` 会按用户明确意图尝试恢复 WeClaw。

已经成功持久化的 remote owner 不因后续普通消息的探测超时或运行通道异常而变化。普通消息只提示运行通道暂不可用；只有显式释放或新的选择接管事务可以修改 owner。

### 8.3 进程崩溃窗口

外部运行时变更与本地状态文件无法组成真正的跨进程 ACID 事务。实现采用 saga：先完成实际运行时移交，再原子持久化，失败时逆序补偿。

如果进程在两者之间被强制终止，重启后以持久化控制意图为期望状态。普通消息不根据进程存在或超时自行决定控制方；用户重新选择或发送 `/cx owner remote` 时可恢复 WeClaw runtime。

## 9. 用户反馈

成功回复至少包含：

- 已切换并接管。
- workspace 和会话摘要。
- 控制方：当前远程窗口。
- 运行位置：Codex Desktop 或 WeClaw。
- 活动任务状态；活动时说明已开始回传。

失败回复按原因区分：

- 其他远程窗口正在控制，请原窗口先释放。
- 当前远程任务仍在执行，请等待完成或先 `/stop`。
- Desktop 运行位置未确认，本次切换未执行。
- 所有权已被并发修改，请重新查询。
- 移交结果未确认，当前禁止继续写入。

错误回复不得展示其他窗口的用户 ID、route key、conversation ID 或内部 IPC 信息。

## 10. 测试设计

### 10.1 Store 单元测试

- A remote、B desktop 的成功原子交换。
- 全部 touched intents 的 revision 校验与递增。
- 任一 revision 冲突时零状态变化。
- 文件写入失败时内存和磁盘完整回滚。
- 重启加载后选择和所有权一致。
- 历史同 route 多所有权的惰性归一化。

### 10.2 消息层状态机测试

- Desktop 空闲和活动 thread 均可选择并自动接管。
- 活动 Desktop thread 不调用 `UseCodexThread`。
- 其他 route 已拥有目标时拒绝且原状态不变。
- 旧 thread 有活动远程任务时禁止切走。
- B 接管失败、旧 thread 释放失败、持久化失败分别验证逆序补偿。
- 补偿失败后进入 fail-closed。
- 同 thread 重复选择幂等。
- 两个 route 并发争用同一目标时只有一个成功。
- ACP stale thread 不覆盖显式选择。
- 所有锁在超时和失败后可复用。

### 10.3 入口测试

- 飞书与微信文本入口语义一致。
- thread ID、编号、卡片按钮、`/cx cd` 自动选择统一接管。
- `/cx new` 与当前默认 Agent 为 Codex 时的全局 `/new` 创建后自动接管。
- `/cx ls`、`/cx pwd`、`/cx status` 不改变控制权。
- `/cx owner remote` 复用统一事务。
- `/cx owner desktop` 释放后普通消息拒绝，重新选择可以再次接管。
- remote owner 已绑定且 runtime 可用时，连续普通消息不调用 Desktop probe。
- remote owner 已绑定但 runtime unknown/超时时，普通消息不弹 owner 卡、不隐式 Handoff，owner 保持不变。

### 10.4 活动任务测试

- 切换活动 Desktop thread 后立即登记观察器。
- 进度和最终结果各发送一次。
- `/guide` 路由到 Desktop active turn。
- `/stop` 路由到 Desktop active turn。
- 观察启动失败触发事务补偿。
- 任务在探测与观察之间结束时正确回传终态，不误报观察失败。

### 10.5 验证门禁

实现阶段至少执行：

```bash
go test ./messaging ./agent -count=1 -timeout 60s
go test -race ./messaging ./agent -count=1 -timeout 60s
go test ./... -count=1 -timeout 60s
go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## 11. 文档与兼容性

- `README_CN.md` 和 `README.md` 改为“选择即接管，显式释放”。
- `/cx help` 说明 `/cx owner remote` 是重新接管兼容入口，不再要求日常两步操作。
- `docs/AI_CONTEXT.md` 更新消息层所有权入口和状态流事实。
- `tasks/lessons.md` 沉淀“显式选择是权威状态，ACP 只能为空状态回填”的规则。
- 状态 JSON 现有字段足够表达新模型；若实现不增加字段，不提升 schema version。

## 12. 实施边界

所有权事务涉及同一 binding、多个 thread、运行时 registry、活动任务和同一个状态文件，核心实现必须串行完成，不使用多个执行者同时修改相同状态链路。

测试可以按 store、消息入口和 Agent runtime 三类独立运行，但最终必须统一执行 race、全仓测试和 Review Gate。发布不属于本设计阶段；只有实现、验证、审查全部完成后才进入发布流程。

## 13. 验收标准

1. 用户在飞书或微信选择会话后，不再需要额外发送 `/cx owner remote`。
2. 成功回复前，选择、唯一远程所有者、实际 runtime 和活动观察均已确认。
3. 从 A 切换到 B 后，A 已释放，B 由当前窗口控制。
4. 其他窗口不能抢占 B。
5. 活动 Desktop 任务保持原进程执行，并立即向远程回传进度和结果。
6. 当前窗口可以对接管的活动任务执行 `/guide` 和 `/stop`。
7. 任一正常可识别失败不会留下半提交状态。
8. 无法确认补偿结果时拒绝新写入，不静默回退或伪报成功。
9. 显式释放后普通消息不会自动重新接管。
10. stale ACP 映射不能覆盖显式会话选择。
