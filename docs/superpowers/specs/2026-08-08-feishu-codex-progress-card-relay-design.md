# 飞书 Codex 引导接力与可折叠进度卡设计

## 状态

已确认，待实施。

本文定义飞书在 Codex 活动 turn 上发送引导消息时的任务卡接力语义，以及原生进度卡的折叠展示契约。当前产品事实仍以 `docs/AI_CONTEXT.md` 和源码为准；本文描述的行为在实施、验证并合入前不能作为已上线能力。

## 背景

WeClaw 已允许本地 Codex App、受控 CLI 和飞书作为同一 shared app-server 的平等入口。飞书绑定到正在运行的 Codex thread 后，普通消息会通过 `turn/steer` 直接进入当前 turn，不创建第二个任务或第二个 writer。

当前任务卡还有两个体验缺口：

- 引导消息虽然已经送入当前 turn，进度仍停留在较早的任务卡，移动端用户需要回到上方寻找最新状态。
- 飞书任务卡把完整进度时间线直接放在单一 Markdown 元素中，开启完整进度后无法收纳；受跟踪任务的流式更新还会全量更新整卡，不能可靠保留用户手动选择的展开状态。

仓库已经具备会话重新绑定时的进度卡 `reanchor`、旧卡 `Supersede`、最新卡 durable reference 和终态 outbox。本设计复用这些状态机边界，不重新定义 Codex task、turn 或 writer ownership。

## 目标

- 每一条被 Codex 成功接受的飞书引导消息，都在该消息下创建一张新的接力进度卡。
- 新卡继承当前完整进度并成为唯一权威卡；旧卡停止更新，显示“已转移”并自动折叠。
- 不为成功引导额外发送“已发送”文字；新卡本身就是成功反馈。
- 引导或迁卡失败时保留真实错误，不能伪造接管成功或重复发送引导。
- 任务执行中的最新卡默认展开且允许用户手动折叠；已转移、已完成、已停止和失败卡自动折叠。
- 普通流式更新只更新稳定内容组件，不重建卡片结构，不重置用户手动展开状态。
- 迁卡、终态、重复事件和服务重启并发时，任何时刻只有一张权威卡接收后续进度与终态。

## 非目标

- 不创建新的 Codex task、turn、writer lease 或 WeClaw 私有引导队列。
- 不把多条快速引导合并或延迟；每条成功引导都独立产生一张接力卡。
- 不改变 Codex App、受控 CLI 或其他消息前端的平等入口语义。
- 不把最终回答塞回进度时间线；最终结果继续作为独立结果卡发送。
- 不用折叠替代卡片字节上限、时间线条数限制或超长续卡机制。折叠只改变视觉展示，不减少 Card JSON 体积。
- 不要求微信或不支持 CardKit 的平台模拟飞书折叠组件。
- 不增加新的用户配置项；现有进度模式和时间线限制继续决定记录多少内容。

## 方案选择

评估过三种方案：

1. **每条成功引导立即接力，选用。** 卡片始终跟随最新用户输入，任务历史中的每次补充都有明确位置，行为简单且可预测。
2. **短时间合并后只接力一次，不采用。** 卡片较少，但需要延迟、定时器和终态竞争规则，且最后一条消息之前的引导缺少独立接力位置。
3. **始终更新最初任务卡，不采用。** 实现最简单，但移动端仍需回到旧消息，违背本地任务可随时由移动端接手的产品目标。

## 术语与状态

- **引导消息**：在已有活动 Codex turn 上通过 `turn/steer` 接受的普通用户输入。
- **权威卡**：当前 `progressSession` 唯一允许接收后续进度、审批记录和终态的任务卡。
- **接力卡**：引导成功后在该消息下新建并接替权威位置的任务卡。
- **已转移卡**：曾经是权威卡、现已冻结的历史卡。它不是任务终态。
- **结构终态**：完成、停止、失败或已转移。进入结构终态的卡片必须停止 streaming 并默认折叠。

卡片状态与展示规则：

| 状态 | 是否权威 | 默认展开 | 后续进度 | 用户可见语义 |
| --- | --- | --- | --- | --- |
| `thinking` / `streaming` | 是 | 是 | 接收 | 当前任务仍在执行 |
| `superseded` | 否 | 否 | 拒绝 | 已在下方新卡继续 |
| `done` | 否 | 否 | 拒绝 | 任务已完成 |
| `stopped` | 否 | 否 | 拒绝 | 任务已停止 |
| `error` | 否 | 否 | 拒绝 | 任务执行失败 |

## 用户可见流程

1. 用户在飞书发送一条普通引导消息。
2. WeClaw 先调用当前 shared Host 的 `turn/steer`。
3. 只有 Codex 明确接受引导后，WeClaw 才在该消息下创建展开的新任务卡。
4. 新卡以当前 reducer 快照初始化，包含最新状态、完整结构化时间线和思考中标记。
5. 新卡完成持久化并成为权威卡后，上一张卡切换为“已转移”并自动折叠。
6. 后续进度、审批记录和终态只写入新卡。
7. 连续发送第二、第三条引导时逐条重复上述过程，形成顺序明确的接力卡链。
8. 任务完成、停止或失败时，最后一张权威进度卡自动折叠，最终结果仍独立发送。

成功引导不再发送额外文字确认。以下情况保留文字反馈：

- `turn/steer` 失败：立即报告发送失败，不创建接力卡。
- 引导已接受但新卡无法创建或持久化：报告“引导已送达，但任务卡迁移失败”，原权威卡继续更新。
- 当前平台或进度模式没有原生任务卡：保留现有“已发送到当前共享 Codex 任务”确认，避免成功后完全没有反馈。

## 卡片结构

飞书任务卡继续使用 Card JSON 2.0，并采用官方 `collapsible_panel` 容器。参考：[折叠面板](https://open.feishu.cn/document/feishu-cards/card-json-v2-components/containers/collapsible-panel)、[CardKit 流式更新](https://open.feishu.cn/document/cardkit-v1/streaming-updates-openapi-overview?lang=zh-CN)。

任务卡拆成稳定的三个区域：

1. 卡片 header：Agent、工作空间和任务标题。
2. 折叠区外的当前摘要：状态与最新一步，即使面板折叠也能快速判断任务是否仍在执行。
3. `collapsible_panel` 内的完整进度：结构化时间线和思考中标记。

审批记录保持现有独立区域。普通进度事件通过 CardKit 内容更新接口分别更新“当前摘要”和“完整进度”稳定元素；不再因任务卡被 registry 跟踪而每次全量重建整卡。审批结构变化可以全量更新，但必须从 registry 的同一快照重建，不能丢失进度、状态或面板默认值。

展示规则：

- 新建活动卡设置 `expanded=true`。
- 用户手动折叠或展开活动卡后，普通流式内容更新不得改变该客户端状态。
- `Supersede` 和终态更新是明确的结构更新，统一写入 `expanded=false`。
- 当前状态与最新摘要始终位于折叠面板外；完整时间线折叠后仍保留在卡片数据中。
- 现有保守 Card JSON 字节上限和超长续卡逻辑继续生效。

消息层不得通过解析已渲染 Markdown 猜测摘要。`progressSession` 已持有结构化 `progressCardSnapshot`，应通过一个可选的结构化流式展示接口同时传递 `Summary` 与 `Details`；不支持该接口的平台继续接收原有单字符串 `Stream.Update`。

## 引导与迁卡数据流

普通活动消息入口和暂存引导入口必须在 Codex 接受后汇入同一个 helper，避免两条路径继续产生不同反馈：

```text
平台消息去重
  -> 读取并确认活动 thread / turn
  -> 获取 thread 级短控制锁
  -> turn/steer
  -> 读取 active task + progress snapshot
  -> 在当前消息下创建新卡
  -> 持久化新卡引用和待收敛旧卡操作
  -> 原子切换 progressSession 权威 stream
  -> 幂等收敛旧卡为 superseded
```

具体约束：

- 复用平台入站消息 ID 去重；同一消息不得重复 steer 或重复建卡。
- 同一 thread 的“steer + reanchor”使用短临界区串行，连续消息按 WeClaw 接受顺序逐条完成，不建立长生命周期 pending queue。
- `turn/steer` 成功是迁卡前置条件；迁卡失败不得重发引导，避免 Codex 重复接收。
- 新卡使用触发本次引导的 `Replier` 创建，因此自然锚定到该引导消息。
- 新卡打开并持久化成功前，旧卡仍是权威卡。
- 权威 stream 切换后，旧卡即使收敛 API 暂时失败，也不得再接收任何进度或终态。
- 会话重新绑定触发的既有 reanchor 继续保留，但与引导迁卡复用同一底层事务和恢复机制。

## 原子性与跨重启恢复

`progressSession.streamMu` 继续作为进度流切换与终态声明的互斥边界。迁卡事务按以下顺序执行：

1. 在锁内确认任务未完成、未 claim 终态且当前 stream 可 supersede。
2. 根据最新 reducer 快照创建新 stream。
3. 导出新 stream durable reference，并在现有 active-stream outbox reservation 中原子写入：新权威引用、旧卡待收敛引用和稳定迁移操作 ID。
4. 更新旧 `Replier` 的 task-card binding，并把 `progressSession.stream`、`reply` 和恢复回调切到新卡。
5. 使用持久化操作 ID 把旧卡停止 streaming、标记为 `superseded` 并折叠。
6. 旧卡收敛成功后，从 reservation 清除对应待办。

现有 terminal outbox reservation 需要扩展为同时保存少量“待收敛旧卡”操作。它仍只保存平台自描述引用、幂等操作 ID 和固定提示，不保存 Token、凭据或 Codex 协议正文。重启时先恢复最新权威卡，再重放尚未完成的旧卡收敛；同一操作可重复投递但只能得到同一最终卡片状态。

终态与迁卡竞争遵循“先 claim 者获胜”：

- 终态先获得 `streamMu`：迁卡返回未移动，不创建新卡，终态在原权威卡收敛。
- 迁卡先完成权威切换：终态只能从新 stream 生成 checkpoint，旧卡不会收到完成或失败状态。
- 新卡已经持久化但旧卡更新失败：不回滚权威卡，后台按持久化操作重试旧卡收敛。

## 错误处理

| 场景 | 行为 |
| --- | --- |
| Codex runtime 或 active turn 不可确认 | 不 steer、不迁卡，返回真实错误 |
| `turn/steer` 拒绝或超时 | 不迁卡，原卡保持权威 |
| 引导成功但新卡创建失败 | 原卡继续更新，提示迁卡失败，不重发引导 |
| 新卡创建成功但 durable reference 持久化失败 | 不发布权威切换；新卡标记迁移失败，原卡继续更新 |
| 权威切换后旧卡收敛失败 | 新卡继续作为唯一权威，持久化重试旧卡，不回滚 |
| 重复平台消息 | 入站去重后不重复 steer 或建卡 |
| 终态先于迁卡 | 不建接力卡，终态只投递一次 |
| 迁卡先于终态 | 终态只投递到新卡 |
| 普通组件更新部分失败 | 保留最新内存快照，下一次更新补齐；不把显示失败升级为 Codex 任务失败 |
| Card JSON 超过限制 | 继续走现有超长续卡，不通过折叠绕过限制 |

所有迁卡结果写入现有 trace：至少区分 `guide.accepted`、`task.card_reanchor_started`、`task.card_reanchored`、`task.card_supersede_pending` 和失败原因。日志和 outbox 状态不得记录引导正文。

## 文件级实施范围

预计修改：

- `platform/reply.go`：增加可选的结构化进度更新能力，以及可持久化的旧卡 supersede 操作契约；保留现有 `Stream` 兼容路径。
- `messaging/codex_task_types.go`：把稳定入站消息身份传入 Codex 引导路径，用于迁卡幂等与 trace。
- `messaging/codex_task_start.go`、`messaging/task_commands.go`：两个 steer 成功入口统一调用迁卡 helper，成功建卡时取消额外文字确认。
- `messaging/progress.go`：复用并加强 `reanchor` 事务，传递结构化摘要/详情，维护唯一权威 stream。
- `messaging/task_state.go`：从现有 reducer 快照导出接力卡所需的结构化展示数据，不解析 Markdown。
- `messaging/terminal_outbox.go`：在 active-stream reservation 中持久化并重试待收敛旧卡操作，保持旧状态文件向后兼容。
- `feishu/card.go`：构建稳定摘要、`collapsible_panel`、完整进度和审批区域，并按状态设置默认展开值。
- `feishu/task_card.go`：保存摘要、详情和折叠结构所需的 card snapshot。
- `feishu/stream.go`：组件级普通更新、结构终态自动折叠、durable supersede 和旧引用兼容恢复。
- 对应测试文件，以及实现完成后的 `README_CN.md`、`README.md`、`docs/AI_CONTEXT.md` 当前事实同步。

若实现中 helper 足够小，可放在现有 Codex task 文件中；只有能隔离“引导成功后的展示迁移事务”时才新增 `messaging/codex_guide_reanchor.go`，不为文件数量单独制造抽象。

## 测试与验收

### 自动化测试

- 卡片 JSON：活动卡 `expanded=true`；已转移、完成、停止和失败卡 `expanded=false`；摘要位于折叠区外，完整进度位于面板内。
- 组件更新：普通进度只更新稳定摘要与详情元素，不全量重建卡片；结构终态执行完整更新并折叠。
- 引导路径：每条成功引导只调用一次 steer、只创建一张新卡、不发送成功文字，并让上一张卡进入 `superseded`。
- 降级路径：没有原生进度卡时保留成功文字；steer 失败不建卡。
- 迁卡失败：创建失败、持久化失败和旧卡更新失败分别保持规定的权威卡与反馈。
- 并发矩阵：连续引导保持顺序；reanchor 与 complete、stop、fail 同时发生时只有一个终态和一张权威卡。
- 幂等与恢复：重复消息不重复 steer；旧卡收敛操作可重放；重启后只恢复最新权威卡并补齐旧卡状态。
- 回归：会话重新绑定迁卡、审批记录、超长续卡、终态 outbox 和非飞书平台行为保持有效。

### 验证命令

```bash
go test ./feishu ./messaging ./platform -count=1 -timeout 180s
go test ./... -count=1 -timeout 180s
go test -race ./... -count=1 -timeout 240s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

正式发布前继续使用 `scripts/release.sh` 的完整门禁。

### 飞书端验收

桌面端和移动端分别验证：

- 活动卡默认展开，可以手动折叠和重新展开。
- 手动折叠后，持续流式更新不会自动展开或重置状态。
- 连续三条引导形成三张顺序正确的接力卡；任一时刻只有最后一张继续更新。
- 前两张卡均显示“已转移”并折叠；任务完成后最后一张也自动折叠。
- 最终结果只出现一次，且仍作为独立结果卡发送。
- 模拟一次旧卡更新失败和一次服务重启后，最新卡继续更新，旧卡最终收敛为“已转移”。

## 实施顺序与回滚

1. 先用 Card JSON 和 adapter 测试锁定折叠结构、组件更新及 v1 durable reference 兼容。
2. 扩展 active-stream reservation 和旧卡幂等收敛，再加强 `progressSession.reanchor` 原子事务。
3. 把普通消息与暂存引导两个成功入口接入统一 helper，补齐顺序、失败和终态竞争测试。
4. 同步当前事实文档，执行全仓验证和飞书桌面端/移动端验收。

回滚时先停止从引导入口触发新 reanchor，并恢复现有单 Markdown 卡片构建；保留扩展 outbox 字段的向后兼容读取，不删除状态文件。已有活动任务继续使用其当前 durable reference 完成，不强制重启、不删除卡片，也不重发任何已接受的引导。
