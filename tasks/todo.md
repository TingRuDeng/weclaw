# 当前任务记录

## 2026-08-07 Codex 无阶段自然语言进度回归修复

### 目标

恢复 Jump 真实 Codex 协议中未携带 `phase` 的用户可见中间消息：从第一条到最后一条中间说明持续写入飞书 `stream` 时间线；正常终态前的最后一条回答仍只通过独立结果卡交付。

### 范围与验收标准

- 明确标记为 `commentary` 的消息继续立即进入进度时间线，明确标记为 `final_answer` 的消息继续禁止进入。
- 对 `phase` 为空的已完成 Agent 消息保留一条延迟判定：后续出现另一条消息、计划、文件、工具、审批或其他继续执行事件时，上一条判定为中间进度并原文展示。
- 正常 `turn/completed` 前仍待判定的最后一条无阶段消息视为最终回答，不写入进度卡；最终回答继续由独立富文本结果卡发送。
- 同一消息 ID 的重复快照只更新待判定内容，不制造重复进度；受控 turn 与接管/恢复 watcher 使用同一判定语义。
- `stream_timeline_limit=0` 继续表示从第一条累计到最后一条中间进度并参与自动续卡；显式正数窗口和 Claude 行为不变。
- 不恢复命令执行摘要、原始工具参数、逐 token、内部推理或最终回答到进度卡。

### 实施步骤

- [x] 先补受控 turn 与接管 watcher 的无阶段消息失败测试，确认当前实现会丢失自然语言进度。
- [x] 实现单条延迟判定并复用现有 commentary 投影，保持显式阶段与最终回答边界。
- [x] 执行 Agent、消息层定向测试，确认完整累计、正数窗口及最终结果独立投递没有回归。
- [x] 执行全仓验证与差异复核；验证通过后再按用户后续指令决定是否更新 Jump。

### 验证方式

- `go test ./agent -run 'TestACPAgentCodexUnphased|TestCollectAttachedCodexUnphased|TestACPAgentCodexCommentary' -count=1`。
- `go test ./agent ./messaging ./feishu -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./agent ./messaging ./feishu -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验与 `git diff --check`。

### 回滚策略

仅回滚无阶段消息的延迟判定状态；显式 `commentary`、最终回答独立投递、时间线无限窗口、自动续卡和 terminal outbox 均保持不变。不得通过直接转发所有无阶段消息来恢复进度，否则会重新把最终回答写入任务卡。

### Review 小结（2026-08-07）

- Jump 真实 rollout 已确认同一 turn 的用户可见 Agent 消息普遍没有 `phase`；原实现只接收显式 `commentary`，因此在时间线条数限制生效前就丢失了全部自然语言进度。
- 受控 app-server turn 与接管 watcher 现在共用单条延迟判定：后续事件证明仍在执行时，上一条无阶段消息转换为 `ProgressKindCommentary`；正常完成前最后一条仍只作为最终回答。显式 `commentary` 和 `final_answer` 语义保持不变，同一 ID 的更新只投影最新正文一次。
- `commandExecution` 仍不产生用户可见 `ProgressEvent`，只发送不含命令、输出和正文的内部活动信号，以便首条自然语言说明在命令开始后及时落卡；审批继续走独立交互路径。
- TDD RED 真实复现受控 turn 和接管 watcher 都只剩文件进度，以及相同消息 ID 被重复投影；实现后相关测试转绿。既有测试继续确认 `stream_timeline_limit=0` 保留超过 8 条的完整 commentary 时间线、正整数窗口仍有效、最终回答独立投递。
- 已验证：受影响包与全仓测试、`go test -race ./agent ./messaging ./feishu`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验和 `git diff --check` 全部通过。Jump 已安全更新为 `v0.1.254-jump.20260807184714.785e0c3.unphased-progress`：系统级 systemd active/enabled、`NRestarts=0`、健康端点与受保护 runtime API 正常、活动任务与 outbox 均为 0，Doctor 关键项通过。回滚文件为 `/home/debian/.local/bin/weclaw.rollback-20260807184714`；尚未通过真实飞书新任务验收自然语言时间线。

## 2026-08-07 飞书任务卡活跃态精简

### 目标

飞书 `stream` 任务在 Agent 首次产生有效非命令进度前，卡片正文只显示“思考中.....”；收到说明、计划、文件修改或工具摘要后重写同一卡片，保留累计回复和结构化进度，并把“思考中.....”固定在正文底部。任务终态移除活跃提示，只保留过程内容和终态状态。

### 范围与验收标准

- 任务卡创建后、首条有效非命令进度到达前，正文只显示“思考中.....”，不得被“等待 Agent”“连接正常”或定时阶段提示覆盖。
- Codex commentary、Claude message、计划、文件修改或工具摘要任一到达后，同一卡片展示截至当前的回复与安全结构化进度；“思考中.....”只出现一次且位于正文最底部。
- 命令、内部推理、状态心跳和审批事件不能解除等待态；Codex `commandExecution` 生命周期仍不进入进度卡，审批卡与审批记录不受影响。
- 后续回复和结构化进度继续原位更新。
- 飞书活跃任务卡不再额外占用顶部状态行重复显示“思考中”；完成、失败、停止仍显示明确终态状态。
- 任务终态正文不得残留“思考中.....”；没有可展示过程时使用明确的“本任务未产生结构化进度记录”说明，最终回答继续由独立结果卡交付。
- 自动续卡、完整卡片容量预检、活动卡恢复引用和终态双路独立重试语义保持不变。

### 实施步骤

- [x] 先补回复前、首次回复、终态和飞书卡片布局的失败测试，确认旧行为可复现。
- [x] 收敛消息层活跃态渲染与定时心跳，只在首条有效非命令进度后刷新进度正文。
- [x] 调整飞书任务卡的活跃状态布局与终态快照，确保活跃提示置底且终态移除。
- [x] 同步中英文 README、`docs/AI_CONTEXT.md` 和长期经验，复核没有恢复命令生命周期噪音。
- [x] 执行定向、全仓、race、vet、tidy、Staticcheck、文档与差异验证。
- [x] 等待 Jump 当前任务自然结束，再安全更新并检查 systemd、doctor 和 outbox；不得强制重启。

### 验证方式

- `go test ./messaging ./feishu -run 'TestNativeStreamShowsOnlyThinkingBeforeFirstAgentReply|TestNativeStreamShowsFirstEffectiveNonCommandProgress|TestTaskProgressUpdateHasEffectiveProgressAcceptsNonCommandEvents|TestFeishuTaskStreamPlacesThinkingAtBottomUntilTerminal' -count=1`。
- `go test ./agent ./messaging ./feishu -count=1 -timeout 180s`、`go test ./... -count=1 -timeout 180s`。
- `go test -race ./agent ./messaging ./feishu -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

回滚活跃提示渲染和飞书任务卡布局即可恢复旧展示；不得删除 terminal outbox、活动卡恢复引用、审批记录或 Agent 会话数据。Jump 更新只在没有活动任务时进行，并保留当前二进制回滚副本。

### Review 小结（2026-08-07）

- 飞书原生任务卡创建后只保留单一正文“思考中.....”，不再显示独立的顶部“思考中”状态行，也不会被定时“等待 Agent / 连接正常”提示覆盖；审批记录仍使用独立区域。
- 首条有正文的 Codex commentary、Claude message、计划、文件或工具摘要会解除等待态并重写同一卡片；命令、内部推理、状态心跳和审批事件不会触发。后续进度继续更新，活跃提示始终只出现一次并位于正文底部。
- TDD RED 复现文件进度不能刷新以及 plan/file/tool 判定为 false；最小白名单实现后定向用例转绿。最终源码的全仓测试、受影响包 race、vet、tidy、Staticcheck、文档校验与 `git diff --check` 均通过。
- Jump 当前任务自然结束且终态 outbox 清空后，已安全更新为 `v0.1.254-jump.20260807172825.785e0c3.noncommand-progress`。systemd 为 active/enabled、`NRestarts=0`，runtime 为 `active_tasks=0`，doctor 关键项正常；回滚副本为 `/home/debian/.local/bin/weclaw.rollback-20260807173100`。

## 2026-08-07 Codex stream 全量用户可见进度

### 目标

恢复 Codex 原生 `stream` 的连续展示体验：从第一条到最后一条累计保留 Codex 明确标记为 `commentary` 的用户可见消息，并把 `stream_timeline_limit` 缺省值改为 `0`。最终回答继续通过独立结果消息交付。

### 范围与验收标准

- Codex `agentMessage.phase=commentary` 按事件顺序进入任务时间线并保留完整正文；相同 ID 的更新仍原位合并，不只保留最新“当前说明”。
- 缺省和未显式配置的 `stream_timeline_limit` 均为 `0`，表示不设置 WeClaw 条数上限；显式正整数仍只保留最近 N 条。
- Codex commentary 与计划、文件和工具摘要共同参与飞书卡片容量预检及自动续卡，跨多张编号卡片形成逻辑完整记录。
- 最终回答、内部推理、逐 token 增量、命令输出、工具原始参数和协议日志仍不得进入进度时间线。
- Claude 当前的独立“当前说明”行为保持不变；结构化计划、文件和工具条目继续使用既有 180 字符收敛，Codex commentary 不按该限制截断；Codex `commandExecution` 生命周期不进入进度卡，命令审批仍独立展示。
- 任务终态只改变卡片状态并保留各段内容；最终结果仍通过独立静态富文本结果卡发送。
- 本轮只修改本地实现、测试和说明，不提交、推送、发布或部署 Jump。

### 实施步骤

- [x] 先补默认值为 `0`、Codex commentary 累计和完整正文保留的失败测试，并确认旧实现按预期失败。
- [x] 在进度事件契约中标记需要累计展示的 Codex commentary，保持 Claude message 兼容语义不变。
- [x] 修改 reducer、时间线渲染与续卡输入，使 Codex commentary 从首条累计到末条并参与容量分段。
- [x] 更新中英文 README、`docs/AI_CONTEXT.md` 和相关旧测试，纠正“只保留当前说明/默认 8 条”的旧结论。
- [x] 执行定向、全仓、race、vet、tidy、Staticcheck、文档和差异验证，再做交付前独立复核。

### 验证方式

- `go test ./config ./agent ./messaging -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./config ./agent ./messaging ./feishu -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

回滚累计标记和默认值即可恢复原有“最新当前说明 + 最近 8 条结构化进度”行为；不得删除 terminal outbox、活动卡恢复引用或已保存的会话数据。若真实平台出现单段异常，保留自动续卡和可观察错误，不能通过静默截断或把最终回答写回进度卡降级。

### Review 小结（2026-08-07）

- Codex `agentMessage.phase=commentary` 现在映射为独立的 `ProgressKindCommentary`，从第一条到最后一条按来源顺序进入任务时间线并保留完整 Markdown 正文；相同 ID 仍原位更新。Claude `ProgressKindMessage` 继续使用独立“当前说明”和 180 字符收敛。
- `stream_timeline_limit` 的默认值及缺省回退均改为 `0`；显式正整数仍对结构化摘要与 Codex commentary 的组合时间线保留最近 N 条。计划、工具和文件摘要继续按 180 字符收敛，最终回答、推理、逐 token、原始输出和工具参数仍不进入进度卡。
- Codex commentary 已纳入既有完整卡片预检与自动续卡：回归测试确认旧卡保留前段并提示第 2 张卡片，新卡从后续 commentary 继续，终态仍只收敛最新卡并独立交付最终结果。
- Jump 真机 Trace 复核发现命令生命周期在最近样本中形成 `292` 个摘要完全相同的 `command` 事件；已在 Codex 事件源停止投影 `commandExecution`，避免其占用时间线和续卡容量，审批事件不受影响。
- TDD RED 已分别复现默认值实际为 `8` 和缺少 commentary 累计语义；实现后定向测试转为 GREEN。全仓测试、受影响包 race、vet、tidy、Staticcheck、文档校验与 `git diff --check` 均通过。
- 独立复核结论为有条件通过：未发现阻止本地交付的行为、安全或兼容性问题；本轮没有提交、推送、发布或部署 Jump，也尚未用真实飞书验证多段 commentary 的视觉效果和真实 CardKit 容量续卡。上方旧任务中“commentary 只进入当前说明、默认 8 条”的结论已被本次确认后的需求修正取代。

## 2026-08-07 飞书独立富文本结果卡与完整进度语义复核

### 目标

让飞书 `stream` 任务在保留原进度卡的同时，通过一条新的静态富文本结果卡交付成功、失败或停止结果；保持未读通知、终态双路独立重试和长内容完整交付。复核 Jump 真机“进度不完整”现象，确认 `stream_timeline_limit=0` 保存的是语义合并后的结构化进度，而不是原始协议日志。

### 范围与验收标准

- 飞书原生 `stream` 任务终态继续只更新原进度卡状态并保留时间线、审批和当前说明；最终回答不得回填原卡。
- 最终结果改为新建静态 CardKit 2.0 风格消息，正文使用 Markdown 元素；标题包含 Agent 与工作空间，完成、失败、停止使用对应状态和颜色，其他平台与非原生 stream 路径保持现有文本行为。
- 本机绝对路径形式的 Markdown 链接改为可复制路径展示，不生成飞书客户端无法打开的伪链接；HTTP(S) 链接保持可点击。
- 结果卡按完整交互消息载荷的保守上限预检并分段，连续编号发送；不得静默截断最终回答。
- 结果卡发送使用 terminal outbox 既有持久记录和稳定 UUID：重试不得重复已成功分段，网络结果不明确时不得改发普通文本制造重复；只有平台不支持富结果能力时才使用现有幂等文本路径。
- 卡片终态和富文本结果继续并行投递，一路失败不得阻止另一条；旧版 outbox 记录仍可加载并按文本恢复。
- Jump 当前两个飞书账号的 `stream_timeline_limit=0` 保持不变；相同 ID 的运行/完成事件继续原位合并，推理、逐 token、命令输出、工具原始参数和最终回答仍不进入进度卡。

### 实施步骤

- [x] 只读核对 Jump 账号级进度配置，并把用户贴出的最终结果关联到对应 turn，统计原始事件、语义唯一项和终态投递状态。
- [x] 先补富文本结果卡 Markdown、状态样式、本地路径处理、载荷分段和稳定 UUID 的失败测试。
- [x] 增加平台可选的幂等终态结果能力，并由飞书 adapter 发送静态结果卡；不改变普通 `SendText` 行为。
- [x] 扩展 terminal outbox 的向后兼容字段，持久化结果标题和富结果意图，并保持 checkpoint、结果两路独立投递。
- [x] 更新中英文 README、`docs/AI_CONTEXT.md` 与相关旧测试，明确“独立新消息不等于纯文本”及 `0` 的语义合并边界。
- [x] 执行定向、全仓、race、vet、tidy、Staticcheck、文档和差异验证，再做交付前独立复核。

### 验证方式

- `go test ./feishu ./messaging ./platform -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./feishu ./messaging -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

回滚平台富结果可选能力和飞书静态结果卡发送后，terminal outbox 继续使用原有幂等文本投递；新增可选字段必须保持旧记录可读且不删除待投递数据。不得通过把最终回答写回进度卡、关闭终态重试或暴露原始协议日志来降级。

### Review 小结（2026-08-07）

- Jump 真机账号级 `stream_timeline_limit=0` 已确认生效。用户示例任务的 8 个命令开始/完成事件按 4 个稳定 ID 原位合并为 4 条结构化进度，另有 1 条 commentary 进入“当前说明”；没有计划事件，推理、逐 token、命令输出和最终回答按安全边界不进入任务卡。因此现象不是仍受 8 条上限截断，而是“完整”指语义合并后的结构化进度。
- 飞书原生 stream 终态结果现在通过新的静态 Markdown 卡片交付，标题包含 Agent 与工作空间；完成、失败、停止使用独立样式。本机绝对路径链接改为可复制路径，HTTP(S) 链接保持可点击。
- 结果卡按包含接收目标、UUID 和二次 JSON 转义的完整消息 envelope 使用 24 KiB 软上限预检，超长正文拆成连续编号卡片。每段从 outbox delivery key 派生稳定 UUID；网络结果不明确时保持富结果路径同键重试，平台缺少能力时才走原有幂等文本。
- terminal outbox v1 以可选字段保存结果标题和富结果意图，旧记录继续使用原 `:text` 键；卡片 checkpoint 与结果仍独立并行、分别持久化成功状态，序列化回复器不会丢失新能力。
- 已验证：`go test ./... -count=1 -timeout 180s`、`go test -race ./feishu ./messaging ./platform -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck 全部通过。独立复核结论为有条件通过：未发现阻止本地交付的问题；尚未部署 Jump 或使用真实飞书验证新静态结果卡的通知、超长分段和跨重启恢复，当前工作区也保留此前多批未提交改动。

## 2026-08-07 Codex stream 真实进度恢复

### 目标

恢复 Codex App Server 任务在飞书 `stream` 卡片中的真实中间进度：把协议中的计划与工具生命周期转换为语义合并的结构化时间线，把 `commentary` 阶段消息放入独立“当前说明”区域，并继续保证最终回答只通过终态独立消息交付。

### 范围与验收标准

- Codex App Server 的 `commandExecution`、`fileChange`、`mcpToolCall`、`dynamicToolCall` 和计划事件映射为稳定 ID、类型与运行/完成状态的 `ProgressEvent`；只展示安全摘要，不复制命令输出、工具原始参数、逐 token 内容或协议日志。
- `agentMessage.phase=commentary` 映射为 `ProgressKindMessage`，仅更新独立“当前说明”区域；`final_answer` 和未知阶段不得写入任务卡，避免最终回答泄漏或重复。
- “当前说明”不进入结构化时间线、不占用 `stream_timeline_limit`，也不覆盖、裁剪或替换已经存在的计划和工具进度；任务终态保留卡片上的已有时间线与当前说明。
- 没有结构化进度但存在 commentary 时，stream 卡仍能展示当前说明；两者都没有时，终态继续显示“本任务未产生结构化进度记录”。
- 保持已实现的卡片容量预检、自动续卡、审批绑定、终态卡片与独立结果消息双路持久化/重试语义不变。
- 同步相关中英文说明和 AI 上下文；本轮只完成本地实现与验证，不提交、推送、发布或部署 Jump。

### 实施步骤

- [x] 先补 Codex 工具、计划、commentary 与 final answer 事件映射的失败测试。
- [x] 实现最小协议解析与安全摘要映射，验证相同 ID 的运行/完成事件能够原位合并。
- [x] 先补“当前说明”独立展示、不占时间线和终态保留的失败测试。
- [x] 扩展任务视图 reducer 与 stream 快照渲染，同时保持非 stream 与最终结果路径不变。
- [x] 同步文档和旧测试语义，执行定向、全仓、race、vet、tidy、Staticcheck、文档及差异验证。
- [x] 完成交付前独立复核并记录剩余真机验证边界。

### 验证方式

- `go test ./agent ./messaging ./feishu -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./agent ./messaging ./feishu -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

回滚新增的 Codex 结构化事件映射和任务视图“当前说明”字段即可恢复旧行为；不删除已有 terminal outbox、活动卡引用或续卡状态。若某类协议字段在真实环境中不兼容，应显式停止该类映射并保留可观察诊断，不能退回为把最终回答写入进度卡。

### Review 小结（2026-08-07）

- Codex App Server 的计划更新及命令、文件、MCP、动态工具生命周期现在会产生稳定 ID 和运行/完成状态的安全摘要；原始命令、输出、参数、结果和 diff 不进入进度事件。`agentMessage` 仅在 `phase=commentary` 时形成当前说明，`final_answer` 与未知阶段只参与最终回答组装。
- `ProgressKindMessage` 现在更新独立“当前说明”区域，不进入或占用结构化时间线；单条按 180 字符收敛，结构化时间线、当前说明、自动续卡和终态内容互不覆盖。最新 reducer 快照会直接进入 durable terminal checkpoint，消除异步更新尚未发送时遗漏最后进度的竞态，同时不增加最终消息之前的网络等待。
- TDD 已真实复现三类旧行为：Codex 结构化事件没有投影字段、commentary 不生成 stream 快照、终态 checkpoint 在最后快照尚未发送时正文为空；对应 RED 均在实现后转为 GREEN。
- 已验证：`go test ./... -count=1 -timeout 180s`、`go test -race ./agent ./messaging ./feishu -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验与 `git diff --check` 均通过。
- 独立复核结论为有条件通过：未发现阻止本地交付的行为、安全或兼容性问题；当前工作区保留此前大量未提交改动，本轮未提交、推送、发布或部署 Jump，也未用真实飞书和 Jump 上的 Codex 版本完成端到端验收。

## 2026-08-07 飞书 stream 终态保留与独立结果投递

### 目标

让飞书 `stream` 任务卡只承载任务状态、审批记录和语义合并后的结构化进度；任务结束时保留卡片过程内容并关闭流式状态，同时通过普通消息独立交付成功、失败或停止结果。卡片终态与最终结果分别持久化、重试和跨进程恢复，任一路失败都不能阻止另一条链路。

### 范围与验收标准

- 保留已经实现的 `stream_timeline_limit`、完整卡片 JSON 容量预检和自动续卡行为，不重做时间线窗口与分段机制。
- `ProgressKindMessage` 不进入结构化时间线、不占用条数限制，也不能替换或挤出已有结构化进度；该阶段没有展示缺少明确协议分类的“当前说明”，现已由上方“Codex stream 真实进度恢复”任务补充为只展示明确 commentary 的独立区域。
- 成功、失败和停止终态只更新最新卡片的标题或状态并关闭流式状态，保留当前分段的时间线、审批记录和正文；没有结构化进度时显示明确的空进度说明，不留下空白卡片。
- 飞书启用最终回答独立投递：成功发送完整最终回答，失败发送必要错误，停止发送固定停止说明；每个终态只有一次逻辑结果投递，允许沿用现有文本分片形成多条物理消息，不再补发重复通知。
- 卡片终态和最终结果使用独立持久化投递状态、错误、重试与超时上下文；任一路失败或阻塞不阻止另一条链路，服务重启后只恢复未成功部分，已成功的文本分片不得重复发送。
- 最终结果的文本分片纳入本轮持久化与幂等保证；现有图片和附件投递能力保持不回退，并明确记录其当前尽力投递边界，不在本轮扩张为新的媒体 outbox 协议。
- 自动续卡后继续以最新卡片作为终态、审批和恢复引用；旧卡冻结保存既有分段，不因后续正数窗口裁剪而追溯修改已经发送的历史卡片。
- 同步中英文 README、`docs/AI_CONTEXT.md` 和相关旧测试语义；本轮只做本地实现与验证，不提交、推送、发布或部署 Jump。

### 实施步骤

- [x] 先补 `ProgressKindMessage` 不进入、不占用、不替换结构化时间线的失败测试，再拆分任务视图状态。
- [x] 先补成功、失败、停止以及无结构化进度时保留卡片正文和明确终态的失败测试，再修改飞书终态快照与能力声明。
- [x] 先补卡片失败仍投递结果、结果失败仍更新卡片、独立重试和重启不重复的失败测试，再拆分 terminal outbox 的投递执行与状态。
- [x] 保持最终文本分片的稳定幂等键，移除飞书 stream 终态的重复完成通知；确认续卡后的最新引用仍用于终态恢复。
- [x] 同步用户说明和 AI 上下文，更新仍断言旧终态覆盖行为的测试。
- [x] 执行定向、全仓、race、vet、tidy、Staticcheck、文档和差异验证，再做交付前独立复核。

### 验证方式

- `go test ./messaging ./feishu -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./messaging ./feishu -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

回滚时恢复飞书终态写卡和原 terminal outbox 顺序，但不得删除已有 outbox 文件、活动卡恢复引用或自动续卡状态；新增持久字段必须保持旧记录可读。若两路独立投递出现异常，优先停用新的调度入口并保留未投递记录，不能把结果标记为成功或清理待重试数据。

### Review 小结（2026-08-07）

- 飞书 stream 终态现在只切换完成、失败或停止状态并关闭流式，保留最新分段的结构化时间线、审批和正文；最终回答、失败结果或停止说明通过普通消息独立交付。`ProgressKindMessage` 不进入或替换结构化时间线，后续仅把明确 commentary 展示在独立“当前说明”区域。
- terminal outbox 在同一记录中分别持久化卡片 checkpoint、结果文本及各自 delivered 状态，并发尝试两路投递；卡片失败或阻塞不延迟结果消息，结果失败也不阻止卡片收敛，重启后只重试未成功部分。
- 复核阶段补出并修正了三个恢复边界：任务卡注册继承 CardKit 已启用的 sequence；审批变化在 adapter 内部锁外立即刷新活动卡引用；adapter 无法从引用生成 checkpoint 时保留显式待重试或死信状态，不再误清理卡片阶段。
- 已验证：`go test ./... -count=1 -timeout 180s`、`go test -race ./messaging ./feishu -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验与 `git diff --check` 均通过。
- 独立复核结论为有条件通过：未发现阻止本地交付的行为、安全或兼容性问题；本轮未连接真实飞书验证通知、超长分片和 CardKit 故障恢复，附件与远程图片仍是已说明的 best-effort 边界。未提交、推送、发布或部署 Jump。

## 2026-08-07 stream 完整时间线与会话导航隐藏

### 目标

让 `stream` 任务卡按配置保留本次任务从开始到当前的结构化进度；接近飞书卡片容量时自动续发后续卡片并保留历史；同时为 Codex、Claude 增加只影响 WeClaw 导航的会话移除与恢复能力。不调用 Agent 的归档、删除接口，不修改 Codex/Claude 私有会话历史。

### 范围与验收标准

- `ProgressConfig` 新增 `stream_timeline_limit`：缺省为 `8`，`0` 表示不设置 WeClaw 条数上限，正整数表示最多保留对应条数，负数在配置加载时明确拒绝。
- 该字段沿用现有全局、Agent、平台、飞书机器人账号四层进度配置合并和热加载语义；显式 `0` 必须能覆盖上层正数，不能因零值合并规则退回默认值。
- `stream_timeline_limit` 只控制结构化任务时间线；`max_progress_messages` 继续只限制非 `stream` 模式的消息发送次数，两者不互相替代。单条进度原有长度收敛保持不变。
- 飞书原生任务卡在更新前按完整卡片 JSON 的保守软上限预检；预计超限时冻结当前卡片的已有进度并提示后续卡片，随后新建下一段卡片继续展示，不等待平台拒绝、不静默截断或丢弃历史。
- 自动续卡使用连续段号，后续进度继续更新当前最新卡片；审批绑定、活动卡恢复引用和跨进程终态交付同步迁移到最新卡片。该阶段曾由最新卡承载最终结果，现已由上方新版任务改为卡片只收敛状态、结果另发普通消息。单条已收敛进度仍无法容纳时返回可观察错误，不制造假成功。
- 新增管理员私聊命令 `/cx session remove <编号|threadId>`、`/cc session remove <编号|sessionId>`，把目标加入主机级 Agent 隔离隐藏层；文本列表、飞书卡片、编号、直接 ID 和过期卡片入口都不得重新选择隐藏会话。
- 新增 `/cx session restore <threadId>`、`/cc session restore <sessionId>`，只移除隐藏标记；移除成功回复提供对应恢复命令，保证操作可逆。
- 会话隐藏是主机级导航变更，仅管理员私聊可执行；目标仍被任一窗口绑定、存在运行中或状态未确认任务时失败关闭，要求先切换或新建其他会话。
- 隐藏状态扩展现有 `workspace-registry.json` 导航覆盖层并升级版本；旧版状态可无损加载，写入继续 copy-on-write、原子替换和 `0600`，损坏、未知版本或持久化失败时不发布内存新状态。
- 审计只记录动作、Agent 和脱敏会话标识，不记录会话标题或消息内容；不调用 Codex archive、Claude 私有文件删除或任何物理删除能力。
- 本轮只完成本地实现与验证；不提交、推送、发布或部署 Jump。

### 实施步骤

- [x] 先补配置默认值、显式零值覆盖、正数覆盖和负数拒绝的失败测试。
- [x] 先补 reducer 默认 8、配置上限和 `0` 全量保留的失败测试，再把已解析配置带入每个活动任务的唯一任务视图。
- [x] 先补飞书完整卡片预检与进度自动续卡的失败测试，再实现保留旧段、创建新段、迁移审批和恢复引用的原子续接。
- [x] 先补导航覆盖层版本迁移、会话隐藏/恢复幂等、持久化失败和损坏保护测试，再扩展状态模型。
- [x] 先补 Codex/Claude 管理员私聊、占用拒绝、列表过滤、编号/直接 ID/旧卡绕过和恢复测试，再接入命令路由。
- [x] 同步中英文配置示例、命令帮助和 `docs/AI_CONTEXT.md`，明确 `0` 的平台边界与“仅隐藏”语义。
- [x] 执行定向、全仓、race、vet、tidy、Staticcheck、文档和差异验证，再做交付前独立复核。

### 验证方式

- `go test ./config ./messaging -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./config ./messaging -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

回滚 `stream_timeline_limit` 时恢复固定 8 条 reducer 行为；回滚飞书续卡时保留平台预检并恢复单卡显式报错，不能恢复为静默截断；回滚会话隐藏时移除命令和过滤层，但保留 Agent 原生会话及历史。新版导航状态必须允许从旧版状态加载；若新字段发生问题，优先恢复隐藏记录的只读可见性，不删除 `workspace-registry.json` 或 Agent 私有数据。

### Review 小结（2026-08-07）

- `stream_timeline_limit` 已支持全局、Agent、平台和飞书机器人账号四层合并：缺省 `8`、显式 `0` 不设 WeClaw 条数上限、正数限制最近 N 条、负数在配置校验阶段拒绝；`max_progress_messages` 的非 stream 语义未改变。
- 飞书任务卡按完整卡片 JSON 的 2,800,000 字节保守软上限预检；超限前创建连续编号的新卡，只把当前分段放入新卡，旧卡保留原进度并指向后续卡。活动卡恢复引用、当前任务卡和后续审批关联在新卡可流式更新且持久化成功后才迁移；该阶段“最终结果只写最新卡”的行为已由上方新版任务调整为“最新卡只收敛状态，最终结果另发普通消息”。
- 独立复核补出了两个边界并以 RED/GREEN 修正：正数时间线窗口滑动时改用稳定事件锚点保留当前分段历史；CardKit 开启流式失败时不再提前发布新任务卡绑定。
- Codex/Claude 已增加管理员私聊 `session remove/restore`；registry 升级为 v2 并兼容读取 v1，隐藏只影响 WeClaw 文本/卡片/编号/直接 ID/任务入口，不调用 Agent 归档、删除或私有状态修改；绑定或非终态任务会拒绝隐藏，审计只保存会话 ID 摘要。
- 已验证：`go test ./... -count=1 -timeout 180s`、`go test -race ./config ./messaging ./feishu -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验与 `git diff --check` 均通过。
- 独立复核结论为有条件通过：未发现阻止本地交付的行为或安全问题；本轮未连接真实飞书验证超大卡片续接，未部署 Jump，也未提交、推送、发布。

## 2026-08-07 systemd 安全重启与任务卡跨进程恢复

### 目标

修复 systemd 重启或进程异常退出后任务卡长期停留在“执行中”、新进程无法停止旧任务的问题；让官方重启入口在任务门禁内排空或明确拒绝，并让绕过入口的重启也能在新进程启动后把旧任务卡更新为中断终态。

### 范围与验收标准

- 活动任务创建飞书原生进度卡后立即持久化可恢复的 CardKit 引用；正常完成复用同一持久记录交付真实终态，不发送重复中断消息。
- 新进程启动时发现上次遗留的活动任务记录，会把原卡更新为“任务已中断”的停止终态并清理记录；不支持原卡恢复的平台保持现有文本恢复语义。
- 收到 SIGTERM 后先停止接收新任务，再取消并等待现有任务完成终态交付；超过有界等待时间时退出，由下一进程继续恢复遗留卡片。
- `weclaw restart` 识别 systemd 托管实例并走安全排空入口：普通重启在有活动任务时拒绝，`--force` 取消任务、交付终态后再重启；无活动任务时正常重启。
- 直接执行 `systemctl restart weclaw` 虽可绕过 CLI 预检，但 SIGTERM 收尾或下次启动恢复必须消除永久“执行中”卡片。
- `weclaw update --source gitee` 未显式传入 `--restart` 时继续只更新二进制，不触发服务重启。
- 不修改 Agent 私有状态，不删除会话或工作空间；本轮不提交、推送、发布或部署 Jump。

### 实施步骤

- [x] 先补活动进度卡持久引用、异常退出恢复原卡和正常终态复用的失败测试。
- [x] 基于现有 terminal outbox 实现活动任务预留记录，并在正常终态与跨进程恢复之间复用同一记录。
- [x] 先补任务排空、SIGTERM 有界等待、强制取消和 systemd 重启委派的失败测试。
- [x] 实现任务 admission/draining、Handler 优雅停机及 systemd-aware `weclaw restart`。
- [x] 更新 systemd 单元为 foreground、on-failure、显式 SIGTERM 与停止超时，并同步必要说明。
- [x] 执行定向、全仓、race、vet、tidy、Staticcheck、文档和差异验证，再做独立复核。

### 验证方式

- `go test ./messaging ./cmd -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./messaging ./cmd -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

回滚活动任务 outbox 预留、Handler 排空和 systemd-aware 重启委派即可恢复原行为；已有终端 outbox 文件保持向后兼容，不删除未完成记录。若新 systemd 单元出现问题，恢复原单元并 `daemon-reload`，但仅在确认无活动任务后重启服务。

### Review 小结（2026-08-07）

- 飞书原生任务卡创建后会把同卡恢复引用写入 terminal outbox；正常终态复用该 reservation，进程中断后由新进程把原卡更新为 stopped，而不是留下永久“执行中”或补发重复中断消息。
- `restart` 与显式 `update --restart` 通过 loopback 排空入口原子关闭任务接纳；普通模式遇到活动任务拒绝，`--force` 取消并有界等待。systemd 实例委派 `weclaw.service`，supervisor 或直接停止失败时会恢复旧进程 admission。
- 前台进程收到 SIGTERM 后先执行最长 10 秒的任务收尾，再停止平台；unit 使用 foreground、`Restart=on-failure`、SIGTERM 和 15 秒停止超时。普通 `weclaw update` 的不重启语义保持不变。
- 已验证：定向 RED/GREEN、`go test ./... -count=1 -timeout 180s`、`go test -race ./messaging ./api ./cmd ./feishu -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验和 `git diff --check` 均通过。
- 独立复核结论为有条件通过：没有阻止交付的代码或安全问题；本机没有 `systemd-analyze`，且本轮未连接真实飞书、未部署 Jump、未执行真实 systemd 重启，也未提交、推送或发布。

## 2026-08-06 `/cc new` 飞书成功状态卡片

### 目标

Claude 新会话创建并绑定成功后，飞书使用单张完成状态卡片展示新会话的工作空间、模型、推理强度和运行通道；其他平台保持文本回复，并确保配置字段来自刚创建的真实 ACP session。

### 范围与验收标准

- 仅 `/cc new` 成功结果升级为飞书状态卡片；失败结果仍用文本返回，`/new` 和其他平台继续使用文本。
- 成功结果固定展示工作空间、模型、推理强度和运行通道，不展示或依赖 Claude 私有状态文件。
- 模型和推理强度优先读取新 session 的 `ClaudeSessionConfig`；ACP 未记录对应字段时显示 `未知（会话未记录）`，不得借用 Agent 默认值。
- 飞书 CardKit 不可用或状态卡创建失败时保留文本降级，不影响已经提交的新 session binding。
- 不改变 `/cc ls`、`/cc switch`、工作空间编号以及现有失败回滚语义。

### 实施步骤

- [x] 先补飞书成功卡片、缺失配置文案和其他平台文本行为测试，并确认当前实现按预期失败。
- [x] 补真实 `ACPAgent` 新 session 配置快照测试，确认卡片字段的数据来源契约。
- [x] 实现结构化新建结果与飞书状态卡发送，保持其他平台文本路径。
- [x] 执行定向、全仓、race、vet、tidy、Staticcheck、文档和差异验证。

### 验证方式

- `go test ./messaging ./agent -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./messaging ./agent -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

移除 `/cc new` 的飞书状态卡投递并恢复原文本结果即可；不回滚或删除已经由 ACP 创建并成功绑定的 Claude session，不修改其他平台和会话目录。

### Review 小结（2026-08-06）

- `/cc new` 成功后，飞书现在使用标题为 `Claude 会话` 的单张完成状态卡；卡片固定展示工作空间、模型、推理强度和运行通道，失败结果仍为文本。
- 模型和推理强度从刚创建并绑定的 `ClaudeSessionConfig` 读取；ACP 未记录的字段逐项显示 `未知（会话未记录）`，不借用新会话默认值。
- 非飞书平台和默认 `/new` 保持文本回复；飞书状态卡无法打开时也降级为同一成功文本，已经提交的 Claude binding 不回滚。
- TDD RED 已真实复现旧实现只发文本且缺少模型、推理强度；GREEN 后定向测试、完整 agent/messaging、全仓测试、受影响包 race、vet、tidy、Staticcheck、文档校验和 `git diff --check` 均通过。
- 剩余边界：本轮未连接真实飞书 CardKit 客户端查看视觉效果，未构建测试版、部署 Jump、提交、推送或发布。

## 2026-08-06 用户导航编号改为从 1 开始

### 目标

将 Codex、Claude 的工作空间与会话列表统一改为用户可见编号从 `1` 开始，并保证卡片、文本列表及 `/cx`、`/cc` 的编号参数使用同一套语义。

### 范围与验收标准

- Codex、Claude 工作空间及会话的文本列表和飞书卡片均从 `1` 开始编号，跨页继续使用全局编号。
- `/cx`、`/cc` 的工作空间进入/移除、会话切换、归档和重命名按 `1` 起始编号解析；`0` 不再指向第一项。
- 卡片按钮继续使用稳定的 opaque token、threadId 或 sessionId，不因展示编号变化降低过期卡片与权限校验。
- 模型、账号和依赖选择等不属于工作空间/会话导航的编号保持现有语义，不扩大改动范围。

### 实施步骤

- [x] 先更新卡片、文本导航和真实命令行为测试，确认在当前 `0` 起始实现上按预期失败。
- [x] 集中转换用户编号与内部切片索引，并统一 Codex、Claude 的展示编号。
- [x] 同步帮助提示，执行定向、全仓、race、静态检查与差异复核。
- [x] 构建测试版并在无运行任务时更新 Jump systemd 服务，保留旧二进制备份并复验 API 与 doctor。

### 验证方式

- `go test ./messaging -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./messaging -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

回滚用户编号转换和展示偏移即可恢复 `0` 起始语义；远端部署保留当前测试版二进制备份，回滚时先确认无运行任务，再原子替换并重启 systemd，不修改工作空间 registry 或 Agent 会话数据。

### Review 小结（2026-08-06）

- 用户可见的 Codex、Claude 工作空间与会话编号现从 `1` 开始；命令层集中换算为内部切片索引，`0` 不再选择第一项，模型、账号和依赖选择编号未改动。
- 飞书工作空间按钮继续使用短期 opaque token，会话按钮继续使用稳定 threadId/sessionId；Claude 跨工作空间会话仍显示与 `/cc switch` 一致的全局编号，可能存在间隔。
- TDD RED 真实复现了 `0` 选中首项、`1` 命中第二项和卡片显示 `0. ...`；GREEN 后完整 messaging、全仓测试、messaging race、vet、tidy、Staticcheck、文档校验和 `git diff --check` 均通过。
- Jump 已在 `active_tasks=0` 时原子更新到 `v0.1.253-test.202608061826`；systemd active/enabled、API `status=ok`、doctor 通过，旧版备份为 `/home/debian/.local/bin/weclaw.backup-v0.1.253-test.202608061810-20260806T183002`。
- 剩余边界：尚未由用户在真实飞书客户端点击新卡片验证显示效果；现有 `allowed_workspace_roots` 未配置警告保持不变，本次未提交、推送或正式发布。

## 2026-08-06 YOLO 自动审批卡片收敛

### 目标

当同一操作者在当前飞书窗口切换 `/mode yolo` 时，既有待审批请求继续自动放行，同时把已经发出的审批卡片更新为明确的自动批准终态、移除操作按钮，并把审批记录写入对应任务卡；YOLO 模式下后续自动审批不再弹新审批卡，只追加任务卡记录。

### 范围与验收标准

- 只处理当前操作者、当前 route 中仍待确认且包含明确允许选项的 Agent 授权；普通结构化提问及其他用户或窗口不受影响。
- 自动审批的真实决策先按现有幂等门禁提交给 Agent，飞书卡片更新不得反向撤销、重复提交或改变审批结果。
- 已存在审批面板时更新为 `已自动批准（YOLO）` 终态并移除按钮，同时在对应任务卡追加同一条简洁记录。
- YOLO 模式下后续授权请求不创建审批面板，只在已有任务卡追加自动审批记录。
- CardKit 更新失败必须可观察：审批保持成功，`/mode` 回复说明卡片更新失败，日志与审计记录失败原因但不记录敏感命令正文。
- 不支持该可选能力的平台保持现有自动审批行为，不制造错误提示或额外消息。

### 实施步骤

- [x] 先补消息层测试，锁定既有审批自动放行后触发一次展示收敛、失败只告警以及未来 YOLO 不弹卡但写记录。
- [x] 先补飞书层测试，锁定审批面板终态、按钮移除、任务卡记录和部分更新失败语义。
- [x] 增加最小平台可选接口并接入 pending approval；复用原任务回复器和 CardKit registry，不引入第二条审批决策链。
- [x] 同步中英文说明与项目上下文，执行定向、全仓、race、vet、tidy、Staticcheck、govulncheck、文档和 diff 验证。

### 验证方式

- `go test ./messaging ./feishu ./platform -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`、`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

如卡片收敛出现回归，移除自动审批展示的可选接口调用即可恢复当前“只自动放行并写审计”的行为；不得回滚或重放已经提交给 Agent 的审批决策，也不删除任务卡、会话历史或审计记录。

### Review 小结（2026-08-06）

- `/mode yolo` 仍由原 pending approval 的原子门禁提交唯一允许决策；已发飞书审批面板或独立卡片随后更新为 `已自动批准（YOLO）` 且移除按钮，对应任务卡追加真实选项和脱敏摘要。
- 后续 YOLO 审批不再发送审批卡；任务卡展示通过有界异步更新完成，不阻塞 Agent 决策。切换瞬间注册的新审批会二次检查 mode，卡片发送失败也不会覆盖已经提交的允许决策。
- CardKit 部分或全部更新失败只影响展示：`/mode` 回复报告失败数量，日志保留平台错误，审计只记录 `card_update_failed/timeout/cancelled` 分类，不记录命令正文。
- 已验证：定向 RED/GREEN、`go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、govulncheck、文档校验与 `git diff --check`。
- 剩余边界：本机未连接真实飞书 CardKit 执行审批，没有验证客户端缓存中的旧卡片动画和网络时序；服务未提交、推送、发布或部署。

## 2026-08-06 Codex / Claude 工作空间与会话命名管理

### 目标

按已确认设计，让管理员通过 WeClaw 登记或隐藏 Codex、Claude 的已有工作目录，并让有权访问目标工作空间的用户重命名 Codex thread 或 Claude session；所有写操作复用 Agent 权威协议、现有单 Host 与 writer lease，不直接修改 Agent 私有状态或删除源码、历史。

### 范围与验收标准

- 独立 `workspace-registry.json` 以规范绝对路径保存 registered/hidden 覆盖层，写入原子、权限为 `0600`，损坏或未知版本时失败关闭且不覆盖原文件。
- `/cx workspace add/remove` 与 `/cc workspace add/remove` 只允许管理员私聊；登记不扩大普通用户的 `allowed_workspace_roots` 权限，隐藏目录不能从列表、编号、ID 或过期卡片绕过。
- `/cx rename current|<编号> <名称>` 通过共享 Codex app-server 的 `thread/name/set` 写入并由 `thread/read.name` 确认；Desktop follower 缺失能力时明确拒绝。
- `/cc rename current|<编号> <名称>` 仅在当前 Claude adapter 公布 `rename` 后，通过同一 ClaudeHost、目标 session writer lease 和 `/rename` 执行，并由 `session/list.title` 读回确认。
- 工作空间移除与会话重命名不改变其他前端 binding，不调用 `thread/delete`、`session/delete`，不启动第二个 Codex/Claude writer。

### 实施步骤

- [x] 先补 registry 合并、幂等、持久化失败、损坏保护和权限过滤的失败测试，再实现最小状态层。
- [x] 先补 workspace add/remove 解析、管理员私聊、导航合并和隐藏绕过的失败测试，再接入 Codex/Claude 命令。
- [x] 先补 Codex rename 参数、权威 RPC、busy/unknown/Desktop 边界和读回验证测试，再实现消息路由。
- [x] 先补 Claude rename 能力公布、Host 复用、writer lease、读回验证和 binding 不变测试，再实现消息路由。
- [x] 同步帮助、中英文 README 与 `docs/AI_CONTEXT.md`，执行定向、全仓、race、vet、tidy、Staticcheck、文档和 diff 验证。

### 验证方式

- `go test ./agent ./messaging ./cmd -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

先停止暴露 workspace/rename 命令并停用 registry overlay，保留 `workspace-registry.json` 供修复版本恢复；不自动删除登记目录、Agent 会话或历史。重命名失败只重新读取 Agent 权威目录并保留原 binding，不写本地标题补偿。

### Review

- 已实现按 Agent 隔离的工作空间登记/隐藏、管理员私聊门禁、全入口隐藏校验，以及 Codex/Claude 权威协议重命名；未修改或删除工作目录、会话历史和其他窗口 binding。
- 已验证：`go test ./agent ./messaging ./cmd -count=1 -timeout 180s`、`go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验与 `git diff --check`。
- 剩余边界：未连接真实 Codex app-server 或 Claude ACP adapter 做线上重命名；运行时继续以能力公布和权威读回失败关闭，不返回假成功。

## 2026-08-06 首次安装依赖选择向导

### 目标

让普通用户首次安装 WeClaw 后立即看到缺失依赖及其用途，按需要选择 Codex、Claude 和辅助能力；安装器在展示完整命令与权限影响并取得确认后，按依赖顺序安装、重新探测并配置可用 Agent。

### 当前事实与推荐交互

- WeClaw 正式二进制本身为静态程序，没有必须额外安装的 Agent 运行时；Node.js/npm、Codex、Claude、Claude ACP、SQLite 和 bubblewrap 是否必需取决于用户选择的 Agent 与功能。
- 首次安装向导先按“所选能力的必要依赖”和“可选增强”分组展示。选择 Codex 或 Claude 后，Node.js/npm 等前置项自动加入并标记为联动必要项，不能静默遗漏。
- `curl | sh` 的标准输入属于安装脚本，交互必须显式连接可用的 `/dev/tty`；没有 TTY 时只打印只读检查结果和可复制的非交互命令，不自动安装任何包。
- 系统包可以在确认后显式使用 `sudo`；npm 包禁止使用 `sudo npm`。当前 npm prefix 不可写时使用用户级目录并通过绝对路径完成能力验证和 Agent 配置，不覆盖用户已有 nvm、mise 或 npm 配置。
- Node.js 版本不足且系统包管理器无法提供满足要求的版本时保留真实失败，提示用户使用其已有版本管理方式处理；不自动添加第三方软件源。
- Claude/Codex 登录、OAuth、API Token 或中继凭据不由安装器自动创建，只在安装和能力检查通过后提示用户完成认证。

### 验收标准

- 交互式首次安装完成二进制校验后，展示缺失组件、用途、必要/可选关系和选择编号；直接回车取消，不产生额外系统或 npm 写入。
- 用户选择 Codex 时按顺序处理 Node.js/npm、Codex CLI 和 `app-server` 验证；选择 Claude 时按顺序处理 Node.js/npm、Claude CLI、固定版本 ACP adapter 和 initialize 验证。
- SQLite 与 Linux bubblewrap 作为可选增强单独展示，不因用户选择 Agent 而被无条件安装。
- 执行前完整显示包管理器、npm 命令、是否需要 sudo 和目标安装目录；用户拒绝时保留已安装的 WeClaw 二进制并正常结束向导。
- 普通用户面对 root 所有的 npm prefix 时不会尝试 `sudo npm`，而是使用受保护的用户级目录；安装后当前进程能解析并保存 Agent 的绝对命令路径。
- 非交互安装必须显式提供组件列表和确认参数；没有 `/dev/tty`、未知组件、版本不足、安装失败或重检失败时均保持可观察错误且不写入假成功配置。
- 已存在且通过能力检查的依赖不重复安装；首次安装和后续 `weclaw doctor --fix` 复用同一组件选择、依赖展开、安装和验证实现。

### 实施步骤

- [x] 先补首次安装交互测试，覆盖 TTY 选择、直接回车取消、无 TTY 提示、组件联动、已有依赖跳过和旧版 Claude 自动安装行为收敛。
- [x] 扩展依赖结果模型与向导输出，明确区分所选能力的必要依赖、可选增强和当前可用项。
- [x] 让 `install.sh` 在二进制安装后通过 `/dev/tty` 进入统一依赖向导；非交互环境只输出带参数的后续命令，不消费脚本输入或默认安装。
- [x] 为 npm 安装增加普通用户目标目录与可写性处理，保持禁止 sudo npm，并用绝对路径完成安装后探测和 Agent 配置。
- [x] 按前置运行时、Agent CLI、ACP adapter、能力探测、配置保存的顺序执行并显示阶段进度；任何阶段失败立即停止后续配置。
- [x] 同步中英文安装说明和项目上下文，移除“检测到 Claude 就未经选择自动安装 ACP”的旧行为描述。
- [x] 执行安装脚本隔离测试、cmd 定向测试、全仓测试、race、vet、Staticcheck、依赖差异、文档校验和 diff 复核。

### 验证方式

- `sh scripts/install_test.sh`。
- `go test ./cmd ./config -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

首次安装向导只编排现有 `doctor --fix` 能力。若交互入口回归，可停止从 `install.sh` 调用向导并保留独立的只读 `doctor` 与显式修复命令；不自动卸载已经由系统包管理器或 npm 成功安装的用户软件，也不覆盖用户原有 Agent 配置。

### Review 小结（2026-08-06）

- 首次安装在二进制摘要校验后先运行只读 Doctor；有控制终端时通过 `/dev/tty` 进入统一选择向导，无终端时只打印安全引用的显式命令。旧版二进制不支持 `doctor --fix` 时只提示更新，不调用未知参数。
- 向导用角色标签区分可选增强、安装前置、可选 Agent 和 Claude 必需 adapter；选择 Codex/Claude 会自动展开前置项，macOS 不展示 Linux 专属 bubblewrap，用户拒绝或直接回车不安装。
- 普通用户的 npm Agent 包使用 `~/.local` prefix，不使用 `sudo npm`；用户可写的 `~/.local/bin` 只在所有安装命令完成后临时加入 PATH，用于重检和保存绝对 Agent 路径，不参与 sudo 或系统包管理器解析。
- 安装脚本 21 个隔离用例、全仓普通测试与 race、vet、`go mod tidy -diff`、Staticcheck、govulncheck、文档校验和 `git diff --check` 均通过；govulncheck 报告调用路径 0 项漏洞。
- 审查结论为通过。本机测试没有执行真实 apt/dnf/Homebrew/npm 安装，也没有进行 Codex、Claude 登录或远端服务变更；外部包管理器行为仍由目标机器环境决定并保持真实失败语义。

## 2026-08-05 运行依赖诊断与交互修复

### 目标

让 WeClaw 在用户实际触发 Agent 或 Codex 会话目录前发现缺失依赖，并在用户明确选择和确认后安装受支持组件、重新探测能力并补齐 Agent 配置。

### 范围与安全边界

- `weclaw doctor` 默认保持只读，检查 `sqlite3`、Linux Codex 沙箱使用的 `bubblewrap`、Node.js/npm、Codex CLI、Claude Code CLI 和 Claude ACP adapter。
- 区分阻塞依赖、可选功能依赖和未选择的 Agent：已配置 Agent 的运行依赖缺失为失败；`sqlite3`、`bubblewrap` 或未配置 Agent 缺失只提示功能影响和可修复入口。
- `weclaw doctor --fix` 仅安装用户选中的缺失组件；交互终端逐项选择并在执行前显示来源、命令和权限影响，默认拒绝。
- 非交互环境必须同时显式提供组件列表与 `--yes`；禁止因为 stdin 不是终端而默认安装全部组件。
- 系统包仅使用已识别平台的原生包管理器和固定参数；需要提权时显式调用 `sudo`，不收集或保存密码。
- Node.js/npm 作为 npm 安装链的条件依赖：Codex 或 Claude ACP 选择 npm 安装路径且本机缺失时自动加入待选依赖，但仍必须由用户在最终确认清单中授权。
- Node.js/npm 只通过已识别平台的原生包管理器安装，并验证上游要求的最低版本；不自动添加 NodeSource 等第三方软件源，不擅自替换 nvm、mise、Homebrew 或其他现有版本管理器管理的安装。
- Codex、Claude 与 Claude ACP 只使用固定的官方发布入口或项目已固定的 adapter 包版本；不接受用户输入拼接包名、URL 或 shell 命令。
- 安装完成后必须重新执行相同能力探测；Codex 验证 `app-server`，Claude 分别验证 CLI 与 ACP 初始化。登录和账号授权不自动执行，只输出后续登录提示。
- 不自动修改系统软件源，不绕过 TLS、签名、摘要或代理策略；原生包管理器不能提供满足版本要求的 Node.js/npm 时保留真实失败，并提示用户通过已使用的版本管理器先完成安装。
- 不在测试中执行真实 `sudo`、包管理器、npm 或远程安装脚本；所有外部动作经依赖注入验证参数、确认门禁和重检行为。

### 验收标准

- 当前 Jump Server 缺少 `sqlite3` 的状态会在 `weclaw doctor` 中提前显示，说明只影响 Codex 会话目录；安装并验证后变为 `[ok]`。
- Linux 上 Codex 可运行但缺少 `bubblewrap` 时显示安全能力降级警告，并可单独选择安装。
- Node.js 与 npm 分别检测命令和版本；仅使用现有 Agent 时不把未使用的 npm 标记为阻塞，选择 Codex npm 安装或 Claude ACP 时会显式展示并联动选择缺失前置项。
- 已配置但缺少 Codex、Claude ACP 等运行命令时保持 `[fail]`；未配置且未安装的 Codex/Claude 只作为可选组件列出。
- `--fix` 只执行选中项；用户拒绝、未知组件、不支持平台、缺少包管理器、安装命令失败或安装后探测仍失败时返回可观察错误，不写入假成功配置。
- Codex 安装后验证 CLI 和 `app-server` 能力；Claude 安装后验证 CLI，再补齐并验证固定版本 ACP adapter，成功后使用现有配置原子写入路径注册 Agent。
- 安装脚本与 CLI 对依赖状态和修复方式的提示一致；中英文使用说明明确安装行为、权限边界、认证边界和非交互参数。

### 实施步骤

- [x] 增加依赖描述与探测层，复用 `config.LookPath`，为 SQLite 数据库、Node.js/npm 版本、Codex `app-server`、Claude CLI/ACP 和 Linux `bubblewrap` 补充无副作用能力检查。
- [x] 先补 `doctor` 结果分级、组件选择、默认拒绝、非交互门禁、平台映射、安装失败与重检失败测试。
- [x] 为 `doctor` 增加 `--fix`、`--components`、`--yes`，解析组件依赖图并展示联动选择，通过固定参数直接调用受支持的包管理器或安装入口，禁止动态 shell 拼接。
- [x] 安装成功后复用正式 Agent 检测和配置保存路径；只在能力探测通过后写入 Codex/Claude 配置，并提示用户完成官方登录。
- [x] 调整 `install.sh`：安装 WeClaw 后只汇总缺失依赖和修复入口，不在 `curl | sh` 默认流程中新增未经选择的系统软件安装。
- [x] 同步 `README_CN.md`、`README.md` 与项目上下文中的依赖、诊断和安装说明。
- [x] 执行定向测试、安装脚本测试、全仓测试、race、vet、Staticcheck、依赖差异、文档校验和 diff 复核。

### 验证方式

- `go test ./cmd ./config ./messaging -count=1 -timeout 180s`。
- `sh scripts/install_test.sh`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

依赖探测、安装执行和 Agent 配置写入保持分层。若修复入口出现回归，可移除 `--fix` 路径并保留只读诊断；任何安装或重检失败均不得写入成功状态或覆盖原有 Agent 配置。系统包和第三方 CLI 由各自包管理器安装，WeClaw 不在回滚时擅自卸载用户机器上已有或新安装的软件。

### Review 小结（2026-08-05）

- `weclaw doctor` 默认只读，提前报告 Codex `app-server`、`sqlite3` 只读 `quick_check`、Linux `bubblewrap`、Node.js/npm、Claude CLI 与 ACP adapter；非原生 `codex-acp` 配置不会被误判为缺少 Codex CLI 的阻断错误。
- `doctor --fix` 支持交互编号选择，以及非交互 `--components ... --yes`；依赖联动、固定参数命令、默认拒绝、安装后重检和 Agent 能力通过后配置均有自动化覆盖，外部安装测试不执行真实 sudo/npm。
- Codex 通过官方 npm 包安装并验证 `app-server`；Claude CLI 与固定 `claude-agent-acp@0.58.1` 使用 npm 且禁止 sudo，Claude 路径要求 Node.js 22+。系统包管理器无法满足版本时在 npm 前失败，不添加第三方软件源。
- 安装脚本 24 个隔离用例、全仓测试、全仓 race、vet、`go mod tidy -diff`、Staticcheck、文档校验和 `git diff --check` 均通过；没有执行真实系统包或 Agent 安装，也没有自动登录外部账号。

## 2026-08-05 GitHub/Gitee 双源安装与更新

### 目标

在保持 GitHub 为权威发布源的前提下，为中国大陆或受限网络用户提供经过同一摘要校验的 Gitee 安装与更新镜像，并让来源选择、切换原因和镜像异常保持显式可见。

### 范围与安全边界

- 安装器支持显式 `github`、`gitee` 与 `auto` 来源；Gitee 用户可从 Gitee 源码镜像取得安装脚本，不再依赖 `raw.githubusercontent.com`。
- `weclaw update` 使用与安装器一致的来源语义；默认来源和环境覆盖必须有明确优先级，不能让未知值静默回落 GitHub。
- `auto` 仅在 DNS、连接、TLS、超时或服务端不可用时切换镜像，并输出切换原因；摘要缺失、重复、格式非法或 SHA-256 不匹配时立即失败，禁止换源掩盖供应链异常。
- GitHub 继续负责版本、构建和权威 Release；Gitee 只镜像同一批二进制与 `checksums.txt`，不在 Gitee 重新构建。
- 正式 GitHub Release 成功后再执行 Gitee 镜像；Gitee 失败不得删除已公开的 GitHub Release，但发布结果必须明确标记镜像降级并返回可观察失败。
- 不引入第三方公共加速代理，不把 Gitee Token 写入仓库、日志、命令输出、Release 资产或配置示例。
- 保留现有 128 MiB 下载上限、临时文件、摘要校验、原子替换、备份和回滚语义；Gitee latest 落后时禁止把现有客户端降级。

### 关键未知与执行门禁

- 目标仓库已确认为公开仓库 `git@gitee.com:jimdeng891/weclaw.git`；当前为空仓库，尚无默认分支或 Release。
- Gitee 官方 API v5 已确认提供创建 Release、上传附件、查询 latest、列出/下载附件端点；写操作需要外部注入访问令牌。
- 发布自动化使用用户创建的 Gitee 私人令牌，权限限制为 `user_info` 与 `projects`；令牌已保存为 GitHub Actions Secret `GITEE_TOKEN`，不在对话、本机配置或仓库中传递明文。
- 本机首次 SSH 探测因 Gitee host key 尚未进入 `known_hosts` 而失败；不使用 `StrictHostKeyChecking=no` 绕过。自动化优先走 HTTPS + Secret，避免把本机 host key 接受作为发布依赖。
- 在令牌安全注入、用户批准本计划且最小实测窗口确认前，不修改实现代码、不推送 Gitee、不上传 Release。

### 验收标准

- GitHub 可用时默认安装与更新行为保持兼容；受控模拟 GitHub 网络失败时，`auto` 明确切换到 Gitee 并完成同一版本安装或更新。
- 显式 Gitee 来源完全不访问 GitHub；显式 GitHub 来源完全不访问 Gitee。
- 两个来源都覆盖四个正式目标和同名 `checksums.txt`；缺失资产、错误摘要、截断下载、超限响应、未知来源和镜像版本倒退均失败关闭。
- `weclaw update` 从 Gitee 下载后仍执行现有启动预检、运行态路径核对、原子替换和失败回滚。
- 发布镜像只接受权威流程已生成并验证的资产；上传后重新下载并校验 tag、资产集合、文件大小和 SHA-256。
- 中英文 README、安装帮助、更新配置、项目上下文和发布运维说明与实际行为一致。

### 实施步骤

- [ ] 确认 Gitee 仓库、令牌最小权限、Release/API 能力和一个不含凭据的测试版本上传/下载流程。
- [x] 先扩展 `scripts/install_test.sh` 与 `cmd/update_test.go`，锁定三种来源、允许换源的网络错误、禁止换源的完整性错误和防降级语义。
- [x] 在 `install.sh` 抽取 release provider，加入来源选择、显式日志、Gitee latest/资产 URL 与同源摘要校验。
- [x] 在 `cmd/update_release.go`、`cmd/update.go` 和配置/Web 映射中加入 update source，复用现有下载上限、摘要校验、原子安装和回滚路径。
- [x] 增加独立的 Gitee 镜像发布脚本或 workflow 步骤：只上传 GitHub 权威流程的现有资产，上传后重新下载验证；不在镜像侧构建。
- [x] 同步 `README_CN.md`、`README.md`、`docs/AI_CONTEXT.md` 和发布运维说明，明确 GitHub 权威、Gitee 镜像及失败边界。
- [ ] 执行安装/更新定向测试、全仓测试、race、vet、Staticcheck、govulncheck、文档校验、diff 检查和两个来源的隔离下载烟测。

### 验证方式

- `sh scripts/install_test.sh`，覆盖 GitHub/Gitee/auto、网络失败切换、摘要失败不切换、架构映射和显式版本。
- `go test ./cmd -count=1 -timeout 120s`，覆盖 provider URL、latest 解析、版本防降级、下载上限、摘要和回滚。
- 使用本地受控 HTTP server 分别模拟 GitHub 与 Gitee，不依赖公共网络制造成功测试或故意超时。
- 对真实 Gitee 测试 Release 仅执行一次最小上传、下载和摘要烟测；删除或保留测试 Release 由用户在执行前确认。
- `go test ./... -count=1 -timeout 120s`、`go test -race ./... -count=1 -timeout 180s`、`go vet ./...`、`go mod tidy -diff`。
- `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`、`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

双源逻辑按安装、更新和镜像发布三个边界隔离。实现回退时保留现有 GitHub 路径和校验语义，移除 Gitee provider/config/workflow 即可恢复单源；任何 Gitee 上传或校验失败都不修改已公开 GitHub Release。镜像凭据只通过外部 secret 注入，撤销凭据即可停止后续镜像，不影响现有客户端使用 GitHub。

## 2026-08-05 Codex provider 会话切换脚本

### 目标

提供一个可重复、可回滚的本机脚本，在 Codex 内置 provider `openai` 与中转站 provider `OpenAI` 之间切换现有会话的归属元数据，并针对已确认的 Responses item ID 类型前缀不兼容提供显式修复能力。

### 范围与非目标

- 新增 `scripts/codex-provider-switch.sh`，只接受大小写精确的 `openai` 或 `OpenAI`，默认只预览，必须显式 `--apply` 才写入。
- 同步修改 `state_5.sqlite.threads.model_provider`、当前与归档 rollout 中的全部 `session_meta.payload.model_provider`，并在 Desktop 本地 thread catalog 存在记录时同步其 provider 缓存。
- 写入前确认 Codex App、app-server 和相关 SQLite/rollout 没有活动 writer，创建带清单和校验结果的独立备份；不自动停止进程、不自动重启 WeClaw 或 Codex App。
- 提供显式 Responses item ID 修复：按条目类型把错误的通用 `item_` 前缀改为 Codex/OpenAI 约定的类型前缀，并保持同一 rollout 内引用一致；不伪造 API 响应、不删除会话正文。
- 不承诺任意 OpenAI-compatible provider 的完整线协议都能跨端点互通；`encrypted_content`、provider 专有 item 类型和工具能力差异仍作为预检告警与剩余风险保留。
- 不修改 `config.toml`、OAuth/API 凭据、模型配置、工作空间绑定、thread ID、会话标题或 Codex App 项目分组。

### 已确认根因

- Codex 的会话发现按 `model_provider` 精确过滤，因此 `OpenAI` 与 `openai` 会形成两个可见性桶；底层 rollout 和 SQLite 数据并未删除。
- 当前失败会话中的 `input[256]` 对应 rollout 内 `type=function_call,id=item_637374b52cfaa643df8fee1e`，而该类型要求 `fc_` 前缀。
- Codex 会把持久化的 `ResponseItem` 重新放入 Responses API `input`；上游现有兼容处理只删除完全无前缀 ID，`item_...` 因包含下划线而被保留。
- 本机当前 25 个会话中有 7 个 rollout、63 个 response item 的 ID 前缀与条目类型不匹配；23 个会话含加密 reasoning，说明 ID 修复不能被表述为通用跨 provider 兼容保证。

### 验收标准

- dry-run 能准确报告目标 provider、SQLite 行数、rollout/session_meta 数量、catalog 缓存数量、ID 修复数量和兼容风险，且不改文件。
- `--apply` 在存在 writer、目标结构不符合预期、JSON 无效、磁盘空间不足、备份或校验失败时保持失败语义并拒绝写入。
- 成功切换后，当前与归档会话的全部 provider 元数据一致，SQLite integrity check 通过，rollout 仍逐行是合法 JSON。
- ID 修复只处理已知 response item 类型的错误前缀，目标 ID 无碰撞，同一 rollout 内相关引用保持一致；再次执行结果幂等。
- 自动化 fixture 覆盖 `OpenAI -> openai`、`openai -> OpenAI`、dry-run、归档会话、多条 `session_meta`、错误 `item_` 前缀、catalog 缓存、重复执行和恢复备份。

### 实施步骤

- [x] 先补临时 Codex home/SQLite/rollout fixture，锁定 dry-run、双向 provider 切换、ID 修复和恢复行为。
- [x] 实现输入校验、只读盘点、writer 门禁、空间检查、备份清单和 staged 原子替换。
- [x] 实现 SQLite、全部 `session_meta` 与可选 catalog 同步，以及类型特定的 item ID 修复与碰撞校验。
- [x] 实现备份恢复入口和幂等校验，不自动操作 Codex/WeClaw 进程。
- [x] 执行 shell 语法/静态检查、fixture 测试、文档校验和 diff 复核。

### 验证方式

- `bash -n scripts/codex-provider-switch.sh scripts/codex_provider_switch_test.sh`。
- `bash scripts/codex_provider_switch_test.sh`。
- 若本机存在 ShellCheck，执行 `shellcheck scripts/codex-provider-switch.sh scripts/codex_provider_switch_test.sh`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。
- 只对备份副本执行真实数据 dry-run；未经单独确认不关闭当前 Codex App、不改写真实 `~/.codex`。

### 回滚与修正策略

每次写入前对 SQLite 使用一致性备份，对所有将修改的 rollout 和可选 catalog 建立独立备份与清单。恢复入口只接受脚本生成且校验通过的备份目录；恢复时同样要求无 writer，并通过 staged 替换和 SQLite integrity check 完成。任何中途失败保留备份和错误日志，不自动重试、不自动启动服务。

### Review 小结（2026-08-05）

- `scripts/codex-provider-switch.sh` 默认只读预览；`--apply` 前同时检查 Codex/WeClaw 进程、目标目录打开文件、SQLite writer、文件归属、磁盘空间和备份目录安全性。
- provider 切换同步覆盖 `state_5.sqlite`、可选 Desktop catalog、当前与归档 rollout 的全部 `session_meta`；显式 `--repair-item-ids` 仅修复已知类型的 `item_` 前缀并保留 `call_id`。
- fixture 覆盖 dry-run、双向切换、归档、多条元数据、ID 引用、幂等、备份恢复、writer 门禁、错误 JSON、ID 碰撞、损坏备份和路径约束；全部测试通过。
- 对真实 `~/.codex` 的临时一致性副本执行 dry-run 成功：复制 27 个 rollout，识别 15 个待改文件、215 条 `session_meta` 和 63 个待修复 item ID；真实目录未写入。
- `bash -n`、嵌入 Python 语法编译、文档校验、fixture 测试与 diff 检查通过；本机未安装 ShellCheck，因此按“若存在则执行”的验证约定未运行。加密 reasoning 仍明确告警，跨 provider 完整兼容不作保证。

## 2026-08-05 飞书凭证权限自动修复

### 目标

在启动和更新后的重启预检中，自动收紧当前用户拥有的飞书凭证目录与文件权限，避免历史目录权限漂移导致服务无法启动。

### 范围与安全边界

- 目录仅允许从可验证的真实目录收紧到 `0700`，文件仅允许从当前用户拥有的真实普通文件收紧到 `0600`。
- 目录属主异常、符号链接、非目录或非普通文件继续 fail-closed，不自动接管或覆盖。
- 更新流程复用启动凭证加载路径，不新增独立的凭证迁移或凭证内容修改行为。

### 验收标准

- 现有 `0755`/`0775` 等当前用户目录可在读取凭证前收紧到 `0700`。
- 现有凭证文件权限过宽时可收紧到 `0600`，文件内容保持不变。
- 缺失凭证仍返回原有登录提示；符号链接和属主异常仍被拒绝。
- 相关单元测试、全仓测试、race、vet、staticcheck、文档校验和 diff 检查通过。

### 实施步骤

- [x] 先补权限自动修复和 fail-closed 回归测试。
- [x] 实现 securefile 权限修复并接入飞书凭证读取路径。
- [x] 执行受影响测试和全仓验证，复核最终 diff。

### 回滚与修正策略

若自动修复边界或启动行为出现回归，只回退凭证读取路径和 securefile 修复 API；不放宽现有属主、符号链接、目录类型和凭证文件安全校验。

### Review 小结（2026-08-05）

- `securefile.RepairPermissions` 只对当前用户拥有的真实目录和普通文件收紧权限；目录变为 `0700`、文件变为 `0600`，不改凭证内容。
- 飞书凭证读取路径在最终 `securefile.Read` 校验前执行安全修复；缺失文件、符号链接、属主异常和类型异常仍保持原有失败语义。
- `go test ./feishu ./internal/securefile ./cmd`、`go test ./...`、核心包 race、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验和 `git diff --check` 均通过。

## 2026-08-05 最新代码审查问题修复

### 目标

修复提交 `967f3ee` 上重新审查确认的 Codex runtime gate、iLink 错误退避、无消息 ID 去重、高权限审计、临时附件、错误脱敏、微信未授权状态、每用户限流、二维码登录退避和 Codex 设计文档漂移问题。

### 范围与非目标

- 范围：`agent/`、`codexauth/`、`ilink/`、`messaging/`、`platform/`、`wechat/`、`feishu/` 及对应测试和 Codex 详细设计文档。
- 不修改 Agent 公共协议、会话 binding、writer lease、平台白名单和管理员授权语义。
- 不处理未能复现的 iLink 孤儿队列推论，不为当前不可达的 `GO-2026-5942` 单独升级依赖。
- 不发布、不重启本机服务、不切换 Codex 账号，也不运行会影响真实 Codex App/daemon 的烟测。

### 验收标准

- 尚未启用多账号索引时，现有非 `0700` `CODEX_HOME` 不再误伤 Codex Host；索引存在时仍严格校验目录、owner、文件和切换 journal，并保留可诊断失败原因。
- iLink 任一业务错误码非零都进入有界退避，不提前重置失败计数或刷新成功活动时间；二维码轮询的即时传输错误也有可取消退避。
- 无稳定 MessageID 的相同文本在首条消息完成任务准入前并发重投时只允许一个 reservation，任务结束后仍允许用户主动重发相同文本。
- `/update`、`/restart`、远程飞书身份授权/撤销、卡片或文本审批及停止动作留下不含凭据和授权短码的结构化审计记录。
- 飞书附件在已验证为当前用户持有的 `0700` 目录内，以 `0600` 排他临时文件承接 SDK 写入；普通 Agent 错误日志统一脱敏并限长。
- 微信 context token 只为已授权身份持久化；授权码状态有明确容量上限；限流键按真实平台账号和操作者计算，不再按飞书群 route 共享。
- Codex 详细设计文档与当前 Desktop IPC / daemon / managed Host 三路径和单一写入权威一致。

### 实施步骤

- [x] 先补回归测试，覆盖 runtime gate、业务错误、去重 reservation、审计、临时目录、脱敏、授权后 token、容量、限流和二维码退避。
- [x] 修复 Codex 账号索引探测顺序，并让 gate 保存和返回失败原因。
- [x] 修复无 MessageID 去重 reservation、真实操作者限流和高权限审计。
- [x] 修复 iLink monitor 与二维码登录的业务错误判断和可取消退避。
- [x] 加固飞书临时附件、Agent 错误脱敏、微信 token 持久化时机和授权码容量。
- [x] 同步 Codex 详细设计文档及文档索引语义。
- [x] 执行定向测试、全仓测试、核心 race、vet、模块差异、文档校验、格式和 diff 复核。
- [x] staticcheck：`/Users/dengtingru/go/bin/staticcheck ./...` 通过，无任何告警；版本为 `2026.1 (v0.7.0)`。
- [x] govulncheck：`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` 通过；调用路径受影响漏洞为 0。

### 验证方式

- `go test` 定向覆盖 `./agent ./ilink ./messaging ./platform ./wechat ./feishu`。
- `go test ./... -count=1 -timeout 180s`。
- `go test -race ./agent ./messaging ./feishu ./ilink ./platform ./wechat -count=1 -timeout 240s`。
- `go vet ./...`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`、`go mod tidy -diff`。
- `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

各修复保持按模块隔离；定向测试失败时只回退对应未通过的最小改动，不放宽现有 fail-closed、安全文件、白名单、管理员、binding 或 writer lease 边界。真实 Desktop IPC 和多用户临时目录攻击无法在单元测试中完全覆盖，将作为剩余实机验证边界报告。

### Review 小结（2026-08-05）

- 新增审批默认拒绝审计覆盖取消和超时分支；卡片/文本审批、管理命令、身份授权/撤销和停止动作均有结构化记录，摘要不包含正文、授权码或命令输出。
- `go test ./messaging -count=1 -timeout 180s`、`go test ./... -count=1 -timeout 180s`、核心包 race、`go vet ./...`、`go mod tidy -diff`、文档校验、`gofmt -l` 和 `git diff --check` 均通过。
- `staticcheck ./...` 与 `govulncheck ./...` 已通过；漏洞扫描报告调用路径受影响漏洞为 0。真实 Codex Desktop/daemon 烟测保持显式未验证，不据此宣称实机运行时验收通过。

## 2026-07-26 Codex 官方 daemon 与账号恢复

## 任务清单

- [x] 区分 SQLite 状态库争用、损坏、明确版本不兼容和未知错误，禁止通用错误或 socket 超时触发 CLI 更新。
- [x] 无权威旧账号回滚点时记录可重试的 `external_sync_deferred`，避免错误进入永久 `rollback_failed`。
- [x] 增加 `codex_host_mode`，在官方 standalone 可用时通过 daemon 生命周期连接唯一 Host，且不回退第二 Host。
- [x] 验证 daemon backend、socket、PID、启动时间、受管路径和 generation，并让 Web 配置完整保留新字段。
- [x] 完成定向、全仓、race、vet、staticcheck、依赖差异、文档门禁、四目标交叉构建和独立交付复核。
- [ ] 在不影响活动任务的隔离窗口执行真实 daemon start/version/stop 与账号切换 smoke；GitHub CI 漏洞扫描已通过，本地 `govulncheck` 仍需在明确允许向外部漏洞库发送依赖元数据后执行。

## 当前状态

实现与本地可执行门禁已完成，独立复核为有条件通过。当前开发机已安装官方 standalone，且现有 WeClaw 后台服务运行正常；本轮没有为验证而中断活动服务，因此隔离的 daemon 生命周期与账号切换 smoke 仍待执行。Codex Desktop 与受管 daemon 的连接状态必须继续独立核验，不能只根据 App UI 推断。
