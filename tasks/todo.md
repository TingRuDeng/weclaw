# 当前任务记录

## 2026-08-12 WeClaw 与 Codex Host 强一致重启

### 目标

让 `weclaw restart` 与 `weclaw update --restart` 在停止服务前收敛 Codex App、受控 CLI、official daemon、managed Host、writer lease 和活动 thread，避免新服务选择另一套 Host 后出现飞书绑定成功但 runtime writer 冲突。

### 完成标准

- [x] 受控 `weclaw codex cli` 全程持共享租约；协调重启持排他租约，不能与新 CLI 启动竞态。
- [x] Codex App、writer lease、active/unknown thread 或不明 Host 存在时，重启在停止 WeClaw 前失败关闭；`--force` 不绕过。
- [x] 只停止身份和 generation 验证通过的 official daemon 或 managed Host；App 只做 IPC/同 UID 主进程存在性探测，不按进程名终止。
- [x] Host 停止前持久化重启状态；新服务在平台监听前验证唯一且 generation 已变化的 Host。
- [x] 外层停止失败时先重建 Host，再删除重启状态并恢复消息准入。
- [x] 完成受影响模块、全仓普通/Race 测试、Vet、module tidy、Staticcheck、文档和差异验证。
- [ ] 发布后在真实 Codex App、受控 CLI 与飞书链路执行端侧验收。

### 验证边界

自动化测试证明状态机、租约、失败关闭和 generation 门禁；不会替代真实 App 退出检测、官方 daemon 生命周期、飞书错误回写和更新重启的端侧验收。

## 2026-08-12 飞书任务卡默认展示最近 5 条进度

### 目标

飞书切换或接管会话后，新任务卡默认在同一正文区域展示最近 5 条语义合并后的结构化进度并实时更新；用户点击“展开完整进度”后，同一区域切换为当前分段的完整进度，后续更新继续写入完整进度末尾。

### 范围与交互约定

- “最近 5 行”按经过 ID/阶段合并后的最近 5 条结构化进度项计算，不按 Markdown 物理换行或逐 token 输出截断；单条仍沿用现有 180 字符收敛。
- 默认态只渲染最近 5 条预览和底部“展开完整进度”按钮，不再额外渲染顶部摘要、第二个进度面板或重复时间线。
- 展开态在原 `main_content` 区域显示当前分段完整时间线，底部只显示“收起完整进度”；后续普通和结构化更新按当前展开状态实时更新同一正文组件。
- 收起后回到截至当前的最近 5 条预览，不隐藏全部进度；折叠期间仍持续积累完整时间线。
- 审批记录、思考中状态、自动续卡、终态保留、独立最终结果、`stream_timeline_limit` 和单条 180 字符限制保持现有语义。
- 容量预检仍以完整展开卡片为准；完整时间线接近飞书限制时继续自动续卡，不能因为默认只显示 5 条而绕过容量控制。
- durable stream reference、终态 checkpoint 和跨进程恢复必须同时保留最近预览所需信息与完整正文；服务重启后按钮状态和后续实时更新不能漂移。

### 验收标准

- [x] 切换到已有 active turn 后，首张卡默认只显示最近 5 条已回放进度；不足 5 条时全部显示。
- [x] 默认态收到第 6 条及后续进度时，原卡正文实时滑动为最新 5 条，不出现第二份重复进度。
- [x] 点击“展开完整进度”后，同一卡片显示从本分段第一条到当前的完整时间线，按钮移到底部并变为“收起完整进度”。
- [x] 展开期间的新进度在完整时间线末尾实时出现；同 ID/阶段更新仍原位更新，不重复追加。
- [x] 点击“收起完整进度”后，同一卡片立即回到最新 5 条预览；再次展开可看到期间积累的全部进度。
- [x] 审批、终态、续卡、服务重启恢复和最终结果独立投递行为不回归。

### 实施步骤

- [x] 先补失败测试，覆盖默认最近 5 条、预览滑动、展开后完整更新、收起恢复预览和无重复区域。
- [x] 在消息层从结构化时间线生成最近 5 条预览，并通过结构化流协议同时传递预览与完整正文。
- [x] 扩展飞书任务卡 registry、CardKit 更新和按钮回调，根据当前展开状态选择同一 `main_content` 的预览或完整正文。
- [x] 扩展 durable reference 与终态/续卡 checkpoint，验证重启后预览、完整正文和展开状态一致。
- [x] 运行 `messaging`、`feishu` 定向测试及与风险匹配的全仓验证，复核最终差异。

### 验证方式

```bash
go test ./messaging ./feishu -count=1 -timeout 180s
go test -race ./messaging ./feishu -count=1 -timeout 240s
go test ./... -count=1 -timeout 300s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### Review

- 飞书任务卡始终只保留一个 `main_content`：默认实时显示当前分段最近 5 条，展开后实时显示完整分段，收起后恢复最新 5 条；切换已有 active turn 的 reanchor 入口已补充直接回归测试。
- `Preview`、完整正文和展开状态已进入 registry、durable reference 与终态 checkpoint；旧引用缺少 `Preview` 时回退完整正文，终态自动收起并移除“思考中.....”。
- 初始卡和后续更新继续按完整展开卡片 JSON 预检容量；审批、续卡、独立最终结果和非结构化平台路径保持原有语义。
- 展开或收起的远端 `UpdateCard` 失败时，registry 仅在 sequence 未变化时恢复原状态，避免下一条流式进度向用户实际仍折叠的卡片写入完整正文，也不会覆盖并发期间的更晚更新。
- 验证通过：`go test ./messaging ./feishu ./platform -count=1 -timeout 180s`、`go test -race ./messaging ./feishu -count=1 -timeout 360s`、`go test ./... -count=1 -timeout 300s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck v0.7.0、文档校验和 `git diff --check`。真实飞书桌面端和移动端交互仍需发布后验收。

## 2026-08-12 Codex App 与飞书真机协作修复

### 目标

修复已在本机 Codex App 和真实飞书端复现的协作断链：飞书选择本地会话后必须持久成为该 thread 的同步前端；Codex App 继续当前任务时，飞书持续收到自然语言进度、审批、问答和唯一终态，并能把新输入 steer 到同一 active turn。

### 已确认事实

- 本机正式二进制为 `v0.1.266`；当前故障不是旧版本或 `progress.mode` 配置导致。
- 飞书会话卡回调经 `inlineCardReplier` 和 `deferredCardResultReplier` 包装后，`codexFollowerFromAcquire` 无法取得底层 `DeliveryRouteReporter`；真机 `codex-sessions.json` 因此为 `FollowRevision: 0`、`Follower: null`。
- 当前 follower 每 2 秒读取一次状态，只在某次读取恰好看到 `Active=true` 时才建立外部任务观察器；快速 turn、观察器建立前的进度和审批存在丢失窗口。
- 重启恢复目前先读取 thread state，后建立 Desktop runtime 映射；新进程在 App Host 上可因未先加载完整 history 而无法重挂观察器。
- 真机 Desktop IPC 稳定返回 `no-client-found: thread stream owner became unavailable`，App 日志同时证明目标 thread 已是 owner。安装包反向点验后确认：Router 已命中 owner handler 并加载历史，但 WeClaw 晚连接而错过一次性 following 询问，未进入 App follower registry，因此 owner 无法广播快照。
- 最新真机任务的 Codex rollout 已分别记录正确的 `commentary` 和 `final_answer`，但 WeClaw Trace 没有 `task.progress`，terminal outbox 投递的正文已经是 commentary；故障发生在 Desktop 状态投影和终态正文组装阶段，不是飞书发送失败。
- Desktop conversation state 中显式 `phase=commentary/final_answer` 的消息可能没有 item `status`；当前投影只在 `status=completed` 时生成 `item_completed`，且 completed turn 只等待一个普通 revision 就发终态，可能在迟到的 final 到达前把 commentary 结算为结果。

### 范围

- 保留真实飞书 `Replier` 的投递能力，使文本命令、会话卡点击和超时后延迟回写都持久化同一账号/会话/消息路由。
- 把 durable follower 与当前 runtime 是否可用解耦：选择成功后即保留 follower；Desktop handler 暂不可用时后台自动重试，可用后不要求用户再次选择会话。
- 建立 thread 级空闲到活动订阅，由 Desktop/app-server 事件立即触发观察，周期快照只作为断线与丢事件恢复，不再是发现 turn 的唯一通道。
- 观察器建立时从当前 active turn 完整快照回放已有的自然语言进度，后续再消费增量事件；命令文本和原始工具日志继续隐藏。
- 修正 pending approval/user-input 的投递确认：没有活动观察器时不得把请求永久标记为已投递；重挂后只投递一次，不自动替用户拒绝。
- 为 follower 增加可持久的 turn/revision 投递游标与稳定幂等键，补齐“快速 turn 在两次状态读取间完成”和服务重启场景，确保进度卡终态与独立结果各自至多成功一次。
- runtime 已确认 active turn 后，飞书普通消息必须携带 expected turn ID 进入同一 `turn/steer`，不新建 turn、不中断 App，不要求先执行 `/cx release`。
- 同步 README、`docs/AI_CONTEXT.md`、单一 Host 设计状态和长期 lessons，删除“仅代码/fake 测试通过即等于真机完成”的错误结论。

### 非目标与安全边界

- 不修改、退出或强制重启 Codex App，不替 App 伪造 stream owner/handler。
- `no-client-found`、断线、超时和身份不明继续失败关闭；不因同步需求启动第二个 Host，不对同一 thread 形成双写。
- 不保存或展示逐 token、原始协议事件、命令行、命令输出或全部工具日志；只同步 Agent 给用户的结构化/自然语言信息。
- 不把订阅成功伪装成 Desktop runtime 已可写；运行通道未建立时保留绑定并清晰显示等待状态。

### 完成标准

- [ ] 飞书会话卡真实包装链执行 `/cx switch` 后，状态文件中产生有效 follower 和递增 revision；重复选择不产生第二条订阅。
- [ ] 飞书在 App 空闲时选择 thread，随后由 App 启动任务，无需再选择即开始收到从第一条可见自然语言进度到最终结果的同步。
- [ ] 选择时 App 已有 active turn，飞书先回放当前 turn 已有的可见进度，然后持续收到增量进度、审批/问答与唯一终态。
- [ ] 飞书在目标 active turn 上发送新消息后，App 与飞书都继续显示同一 turn；不增加第二 turn，不取消或重启原任务。
- [ ] 短 turn 在一次调和间隔内完成时仍投递最终结果；观察器建立前出现的 pending approval/user-input 仍可在飞书处理。
- [ ] 服务重启、Desktop IPC 暂断、重复快照和终态重试不丢投递、不重复最终结果，不把尚在 App 运行的 turn 误报为已停止。
- [ ] Desktop 未建立目标 handler 时保留 binding/follower 并自动重试；不允许飞书写入、不启动第二 Host，App 打开准确会话后自动恢复。
- [ ] 同一 thread 的多个已授权飞书身份不因 Agent 仅允许一个 turn watcher 而互相抢占；进度和终态按各自 durable route 幂等投递，交互回答只能提交一次。
- [ ] 命令文本、命令输出与原始工具日志不进入飞书进度时间线；最终回答继续作为独立消息且只成功一次。

### 实施步骤

- [x] 先补失败测试：真实飞书包装链的 follower 持久化、idle→active→terminal、快速 turn、观察前审批、active 快照回放、断线/重启、多 route 与 steer 同 turn。
- [x] 修正可选 Replier 能力解包和 follower 事务，确保运行通道失败只改变可用性，不清理 durable route。
- [x] 收敛为 thread 级权威事件订阅与 route 级投递，复用现有 external task/progress/terminal outbox，不为同一 thread 启动多个互斥的 Agent watcher。
- [x] 在订阅恢复时先建立/复核唯一 Host runtime 并加载带 revision 屏障的 history，再根据 active/terminal 快照创建观察任务或补投唯一终态。
- [x] 修复 Desktop late-join 握手：仅在 App 是当前权威 Host 时，读历史前主动、幂等地登记 follower 并等待首个 snapshot；daemon 权威时不抢占 App。
- [x] 修正 Desktop 快照投影与事件投递确认，回放 active turn 的全部可见自然语言信息，并保留 pending interaction 直到真实观察端取得它。
- [x] 持久化最小投递游标，把进度卡终态和独立最终消息继续交给 terminal outbox 两路独立重试，并为旧状态文件提供向前兼容读取。
- [x] 打通选择、自动重挂与 active steer 的同一状态机；订阅尚未可写时只显示友好等待状态，可写后自动收敛，不伪造成功。
- [x] 同步当前事实文档，复核实际 diff 与状态迁移，执行定向、全仓、Race、Vet、Staticcheck、文档和差异验证。
- [x] 更新本机 `v0.1.267-rc2` 验收候选并通过非强制排空重启，确认版本、进程和原 durable follower 无需重选即停止 `no-client-found` 循环。
- [x] 先补失败测试：显式 phase 但无 item status 的 commentary/final 投影、多个无 final revision 的终态屏障，以及 commentary 不参与最终正文组装。
- [x] 修复 Desktop 消息投影和终态收敛，确保进度卡只接收 commentary，独立最终结果只接收真实 final_answer。
- [ ] 重新执行 agent、messaging、feishu 定向测试及全仓门禁，构建本机验收候选并在真实飞书复测本轮任务。
- [ ] 使用真实 Codex App 和飞书桌面/移动端执行下述端侧验收；本次用户已明确授权在完整自动化门禁通过后提交、推送和发布，端侧清单继续作为发布后验收边界。

### 验证方式

```bash
go test ./feishu ./messaging ./agent ./platform -count=1 -timeout 180s
go test ./... -count=1 -timeout 180s
go test -race ./agent ./messaging ./feishu -count=1 -timeout 240s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

正式发布前仍以 `scripts/release.sh` 中的完整门禁为准，不用上述定向测试代替。

### 本轮卡片交互与投递日志增量

#### 目标

修正飞书终态投递后的误导日志，收敛完整进度的展开/收起交互，并移除成功卡片正文中与绿色卡头重复的“已完成”提示。

#### 范围

- 当终态 outbox 已完成且没有剩余正文、图片或附件时，直接结束空投递，不再错误记录 follower 授权变化。
- 真实 follower guard 拒绝继续保留，但日志需区分 revision、thread、identity、route 等具体失配原因。
- 完整进度改为服务端持久化的互斥控制：折叠时只显示明确样式的“展开完整进度”按钮；展开后隐藏该按钮，并在完整进度底部显示“收起完整进度”按钮。
- 移除卡片顶部重复的“最近进度”摘要；用户可见区域只保留一份完整结构化时间线。`Summary` 只用于续卡、容量判断和恢复，不再作为第二份进度正文渲染。
- 卡片折叠期间继续在 registry 中积累最新完整进度，但不向已经隐藏的正文组件流式写入；再次展开时一次性呈现截至当前的完整时间线。
- 展开和收起都只更新当前 CardKit 卡片及 registry 状态，不进入 Agent 消息分发；后续进度更新不得重置用户选择。
- 成功终态不再在卡片正文添加“已完成”；卡片颜色和标题继续表达终态，已有进度与审批内容保持不变。失败和停止的现有错误信息不在本轮修改范围内。

#### 验收标准

- [x] 最终结果已发送且无剩余 payload 时，不产生 `reply.delivery.suppressed`；确有 guard 拒绝时日志包含可操作的精确原因。
- [x] 折叠态仅显示“展开完整进度”，展开态仅显示底部“收起完整进度”，两个按钮视觉上均是明确按钮而非普通文本。
- [x] 卡片不再同时显示顶部最近进度和下方完整进度；折叠期间产生的新进度在再次展开后完整可见。
- [x] 点击展开和收起均立即更新同一张卡片，按钮互斥、状态可持续，并且不会向 Agent 发送按钮文本。
- [x] 成功卡片正文不包含重复的“已完成”，结构化进度、审批记录及独立最终结果消息均不受影响。

#### 实施步骤

- [x] 先补失败测试，覆盖空投递日志、精确 guard 原因、展开/收起互斥状态与成功卡片正文。
- [x] 调整 reply projection 的空 payload 短路和 follower guard 诊断语义。
- [x] 收敛完整进度控制状态、CardKit 更新回调和按钮样式，保留后续进度更新时的展开状态。
- [x] 移除用户可见的重复摘要更新，并验证折叠期间的数据积累与重新展开。
- [x] 移除成功卡片正文中的重复终态提示，复核无进度、含进度和含审批三种卡片。
- [x] 运行受影响模块定向测试及当前任务全仓门禁，复核最终差异；本轮未更新本机验收候选。

#### 定向验证

```bash
go test ./feishu ./messaging -count=1 -timeout 180s
go test -race ./feishu ./messaging -count=1 -timeout 240s
```

#### 本轮 Review

- 卡片已移除顶部 `progress_summary` 与下方 `progress_panel` 的双区域渲染，只保留 `main_content` 作为完整进度正文；折叠态隐藏正文并展示展开按钮，展开态在正文底部展示收起按钮。
- 折叠期间仍在任务卡 registry 中更新完整正文；容量预检按展开态完整卡片执行，终态与续卡 checkpoint 保留恢复控制状态所需的最小快照。
- 实际通过飞书/消息模块测试与 Race、`go test ./... -count=1 -timeout 300s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck v0.7.0、文档校验和 `git diff --check`。全仓测试首次在受限沙箱中因 `/bin/ps` 被拒绝而失败，允许进程探测后原命令通过。
- 本轮改动已完成发布门禁，并按用户明确授权纳入本次提交与发布；尚未覆盖本机二进制或重启服务，真实飞书桌面端和移动端交互仍需后续验收。

### Review

- 实现已覆盖飞书卡片回调路由持久化、App/受控 CLI 活动 turn 快照回放、多 route 同步、精确 turn steer、断线重挂、快速终态补投、pending interaction 重放、撤权隔离与结果幂等。
- 实际通过 `go test ./... -count=1 -timeout 300s`、`go test -race ./agent ./messaging ./feishu -count=1 -timeout 360s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck v0.7.0、文档校验与 `git diff --check`。
- 上述证据只证明源码与自动化门禁通过；真实 Codex App、飞书桌面/移动端、IPC 断线与 WeClaw 重启仍需按下述清单验收。
- 首轮独立复核后，真机验收发现并复现 Desktop late-join follower 未登记的 P1；已以稳定红测试固化原错误、实现主动幂等登记，并由独立只读复核确认不会抢占 daemon 权威。无界 observer FIFO 在消费者异常长期停顿时仍有内存积压与交互延迟的 P2 风险。
- 本机先前已安装并启动 `v0.1.267-rc2` 验收候选；原 binding 恢复已越过之前的 App follower 握手故障，但新 turn 的飞书进度、交互和唯一终态仍需用户端到端确认。
- 最新真机截图对应的错位已通过红绿回归固化：状态缺失的显式 commentary 进入进度，显式 final_answer 独立参与最终组装，普通 completed revision 不再提前结束。`go test ./...`、受影响模块 Race、Vet、module tidy、Staticcheck、文档校验和差异检查均通过。
- 已构建 `v0.1.267-rc3` darwin/arm64 验收候选至临时目录；回环 API 仍报告 `active_tasks=1`，因此没有覆盖 `v0.1.267-rc2` 或强制重启。必须等当前任务终态投递后再安全切换并做真实飞书复测。

### 真机验收

当前 v12 状态保留已有 workspace/thread 选择，但旧记录没有可复核授权身份，因此 follower 按 fail-closed 迁移为空；本轮首次验收需在飞书重新选择一次目标会话，之后正常重启不应要求重复选择。

- [ ] App 打开准确 workspace/thread 并启动一个会产生多条自然语言进度的任务；飞书选择同一会话后看到已有进度与后续更新。
- [ ] 任务中触发一次真实审批和一次结构化问答，在飞书回答后 App 继续原 turn，最终只收到一条结果消息。
- [ ] active turn 中从飞书补充一条指令，确认 App 立即继续同一 turn，`/cx status` 的 turn ID 未变且没有第二 writer。
- [ ] 先在飞书选择 App 尚未打开的 thread，再在 App 打开它；不重复选择，确认后台自动恢复同步。
- [ ] 任务执行中重启 WeClaw，确认 App turn 不被中断，飞书进度卡恢复后续写且不补发“已停止”或重复结果。
- [ ] 暂时关闭又重开 App IPC 或切换离开再回到目标 thread，确认绑定保留、等待文案清晰，运行通道恢复后继续同步。

## 2026-08-12 基于最新代码同步项目文档

### 目标

以当前 `main` 的真实源码、命令帮助和运行日志为依据，审查全部受跟踪 Markdown 与上下文包，修正文档漂移并补齐用户可操作的边界说明。

### 范围

- 同步中英文 README 中 Codex App Host 会话绑定、运行通道不可用、`/status` 版本展示和恢复操作说明。
- 在维护者上下文与长期 lessons 中记录 Desktop `no-client-found` 的失败关闭语义，以及 `turn/start` 响应与进度、审批、终态事件可能乱序的生命周期屏障。
- 清理 `tasks/todo.md` 中已经与当前 Git/发布事实不一致的阶段描述，只保留仍需真机验收的边界。
- 逐一复核 `AGENTS.md`、`docs/README.md`、`docs/AI_CONTEXT.md`、三份设计基线、`THIRD_PARTY_NOTICES.md` 和文档校验器；准确内容不做机械改写，历史设计只补必要的状态或勘误说明。
- 不修改实现代码、配置、凭据、运行服务或发布状态。

### 验收标准

- [x] 中英文 README 对当前 Host 选择、会话绑定与故障恢复给出一致且可执行的说明，不把“绑定已提交”写成“运行通道已可用”。
- [x] `docs/AI_CONTEXT.md` 与 `tasks/lessons.md` 准确描述 Desktop handler 缺失和 `turn/start` 事件乱序边界，并保持上下文文档长度契约。
- [x] 当前任务记录不再声称已提交代码“尚未提交、推送或发布”，真实飞书/App/CLI 端侧验收仍明确标为未完成。
- [x] 所有权威文档路径、发布资产矩阵、权限模型、命令列表和验证命令与当前源码一致；无本机绝对路径或敏感信息进入文档。
- [x] 文档编译、上下文校验和差异检查通过。

### 实施步骤

- [x] 只读核对工作树、最近提交、CLI 帮助、全部 Markdown、上下文校验器与当前运行故障证据。
- [x] 更新 README_CN.md、README.md 的用户说明并保持双语语义一致。
- [x] 更新维护者上下文、长期 lessons 和必要的设计基线勘误。
- [x] 收敛当前任务记录，复核全部文档最终差异。
- [x] 运行文档验证并记录 Review。

### 验证方式

```bash
python3 -m py_compile scripts/validate_docs.py
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### Review

- 已按 upgrade 模式审查当前上下文包与全部受跟踪 Markdown；只修改 7 个计划内文档文件，没有改动实现、配置、凭据、服务或发布状态。
- 中英文 README、维护者上下文、长期 lessons 与单 Host 设计基线已经统一会话 binding/runtime 边界、Desktop `no-client-found` 恢复方式、`turn/start` 事件乱序屏障、v12 持久化状态和 `/status` 版本展示。
- `python3 -m py_compile scripts/validate_docs.py`、`PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic` 与 `git diff --check` 均以退出状态 0 完成；`docs/AI_CONTEXT.md` 保持 120 行，最终差异仅包含计划内文档。
- 本轮为纯文档同步，未运行 Go 测试；真实 Codex App、受控 CLI、飞书桌面端/移动端和进程异常退出仍保留为下方端侧验收项，未把自动化结果写成真机结论。

## 2026-08-09 Codex 协作与飞书任务卡端侧验收

### 目标

在真实 Codex App、受控 CLI、飞书桌面端和移动端验证同一 Host/thread 协作、会话绑定恢复、引导接力卡和进度折叠。自动化与正式发布不能替代端侧验收。

### 待验收

- [ ] App 是当前 Host 且目标 thread 已在 App 打开时，从飞书选择同一会话，确认运行通道可用并能持续同步。
- [ ] App 存在但目标会话没有可用 Desktop handler 时，确认飞书显示“已选择，等待运行通道”、普通消息被阻止；在 App 打开准确会话后由后台自动恢复，无需再次选择。
- [ ] 官方 daemon 是当前 Host 时，用 `weclaw codex cli` 打开同一 thread，确认 CLI 与飞书看到同一任务状态并可继续输入。
- [ ] 从飞书发送普通消息或 `/guide`，确认输入进入当前 active turn，不创建第二个 writer 或私有续跑任务。
- [ ] 执行 `/cx release`，确认本地任务继续运行，原飞书窗口不再收到进度、审批、问答或最终结果；重新绑定后从最新快照继续。
- [ ] 连续发送多条成功引导，确认每条消息下各生成一张新接力卡，旧卡显示“已转移”并停止更新。
- [ ] 确认活动卡可手动折叠且普通更新不重置展开状态；完成、失败、停止和已转移卡自动折叠。
- [ ] 在飞书桌面端与移动端分别确认最新卡持续接收进度和唯一终态，最终结果消息不重复。
- [ ] 做一次真实进程异常退出恢复演练，确认 durable binding、活动卡恢复项和 observer 重挂不会把仍在运行的共享 turn 误报为停止。

### 已完成边界

- [ ] 同 Host/thread 协作、活动 turn steer 和 durable follower 重挂的自动化链路已存在，但真机验收已证明卡片路由持久化、空闲订阅和 Desktop 恢复仍有断链；转入上述当前修复任务。`/cx release` 非中断解除和全局 `/stop` 不在本次故障范围内。
- [x] 账号级 `allowed_users` capability、机器人隔离和旧 `admin_users` 忽略告警已实现。
- [x] 引导接力卡、折叠面板、终态收敛、重启恢复和两路独立投递已通过自动化验证。
- [x] 权威发布门禁已验证安装脚本、全仓测试、Race、Vet、Staticcheck、govulncheck、文档、module tidy 和差异检查。
- [x] 相关实现已合入 `main`，`v0.1.266` 指向当前提交 `fa8d16d`；本机正式二进制已更新到该版本。
