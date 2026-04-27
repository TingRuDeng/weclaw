# 微信进度摘要模式落地清单

## 目标

默认关闭微信里的实时正文 delta 流，改为“受理确认 + typing + 低频进度摘要 + 最终完整结果”。旧实时正文片段保留为 `progress.mode=stream` 兼容模式。

## 执行任务

- [x] 梳理现有配置、消息处理、ACP 路由和测试结构。
- [x] 新增 progress 配置默认值、全局配置、Agent 覆盖和环境变量覆盖测试。
- [x] 新增 progress 渲染、节流、去重和模式行为测试。
- [x] 新增 Handler summary / stream / Agent 覆盖 / MessageID 为 0 fallback 去重测试。
- [x] 新增 ACP 多 active turn 禁止任意 fallback 的测试。
- [x] 实现 `config.ProgressConfig`、默认值归一化、Agent 覆盖和 env override。
- [x] 接入 `cmd/start.go`，把全局和 Agent progress 配置传给 Handler。
- [x] 拆分 `messaging/progress.go`，实现 summary 默认模式、stream 兼容、受理确认、typing heartbeat、进度节流和错误文案。
- [x] 改造 `messaging/handler.go`，使用 progress 配置、文本 fallback 去重和最终回复稳定前缀。
- [x] 修复 `agent/acp_agent.go` 的多 active turn 任意 fallback 风险。
- [x] 更新 `README.md` 和 `README_CN.md` 的 progress 配置说明。
- [x] 运行受影响范围内测试和全量 Go 测试。
- [x] 补充 review 小结。

## Review 小结

已完成默认 `summary` 模式、`stream` 兼容模式、Agent 级覆盖、MessageID 为 0 的文本 TTL 去重、ACP 多 active turn fallback 限制和 README 配置说明。验证命令：`go test -count=1 ./...`，结果通过。当前仍未实现 typed event bus、FinalAssembler、运行时 `/progress` 切换、长结果分段和任务状态持久化，这些属于后续阶段范围。

## 长结果分段落地清单

### 目标

最终回复过长时按自然边界拆成多条微信消息顺序发送，避免单条消息过长导致微信展示或发送异常。

### 执行任务

- [x] 梳理最终回复发送路径：`sendReplyWithMedia` 先处理附件和图片，再通过 `SendTextReply` 发送文本。
- [x] 新增长文本分段发送测试，覆盖分段数量、顺序和换行保留。
- [x] 新增 Handler 层测试，确认最终回复会走分段发送。
- [x] 实现 `SendTextReplyChunks`，按段落、行和 rune 上限拆分文本。
- [x] 将最终回复文本发送从单条 `SendTextReply` 改为 `SendTextReplyChunks`。
- [x] 运行全量测试并补充 review 小结。

### Review 小结

已完成最终文本分段发送，默认单条上限为 1800 rune。短回复保持单条发送；长回复按换行优先拆分，单行超长时按 rune 安全拆分。验证命令：`go test -count=1 ./... && git diff --check`，结果通过。

## Codex FinalAssembler 落地清单

### 目标

处理 Codex app-server 同一条回复里 snapshot、delta、completed 文本同时出现时的重复和兜底问题，保证最终回复优先使用 delta，缺少 delta 时再使用 completed 或 snapshot。

### 执行任务

- [x] 梳理 Codex app-server 当前最终回复路径：`chatCodexAppServerWithRetry` 直接 append `Delta` 和 `Text`。
- [x] 新增重复场景测试：同一 item 先收到 snapshot，再收到 delta，最终只使用 delta。
- [x] 新增兜底场景测试：没有 delta 时使用 snapshot。
- [x] 为 `codexTurnEvent` 增加 `ItemID`，让同一 turn 内多个 item 可独立归并。
- [x] 新增 `codexFinalAssembler`，按 item 顺序归并文本，优先级为 delta、completed、snapshot。
- [x] 接入 `item/completed` 事件，作为无 delta 时的 completed fallback。
- [x] 运行全量测试并补充 review 小结。

### Review 小结

已完成 Codex 最终结果归并改造。现在同一 item 同时存在 snapshot 和 delta 时优先使用 delta，避免重复；没有 delta 时使用 completed 或 snapshot 兜底。验证命令：`go test -count=1 ./... && git diff --check`，结果通过。

## Codex 失败信息优化清单

### 目标

把 Codex app-server 返回的账号、工作区和额度类错误转成微信里可理解的中文原因，避免只显示“Codex 返回未知错误”。

### 执行任务

- [x] 新增 `deactivated_workspace` 解析测试，覆盖工作区不可用错误。
- [x] 新增 raw message/code 解析测试，覆盖非 `error.codexErrorInfo` 结构。
- [x] 扩展 `formatCodexError`，支持 `error.message`、顶层 `message/code`、`detail.code/message`。
- [x] 运行全量测试并补充 review 小结。

### Review 小结

已完成 Codex 错误解析增强。`deactivated_workspace` 会展示为“Codex 工作区不可用”，顶层 `message/code` 结构也能保留原始错误信息。验证命令：`go test -count=1 ./... && git diff --check`，结果通过。

## `/progress` 运行时切换落地清单

### 目标

允许在微信里通过 `/progress` 查看当前进度模式，并通过 `/progress <mode>` 临时切换当前进程的全局 progress 模式。

### 执行任务

- [x] 新增查看当前模式测试。
- [x] 新增切换到 `stream` 模式测试。
- [x] 新增未知模式拒绝测试。
- [x] 在内置命令分支接入 `/progress`，避免误转发给 Agent。
- [x] 实现 `off/typing/summary/verbose/stream/debug` 模式校验。
- [x] 运行全量测试并补充 review 小结。

### Review 小结

已完成 `/progress` 运行时切换。该命令只影响当前进程内的全局 progress 模式，不写回配置文件；重启后仍以配置文件为准。验证命令：`go test -count=1 ./... && git diff --check`，结果通过。

## 微信文件输入落地清单

### 目标

支持用户在微信里直接发送文件给 Agent：WeClaw 下载并保存文件，再把本地路径作为消息内容交给 Agent 分析。

### 执行任务

- [x] 梳理当前非文本消息路径，确认 `ItemTypeFile` 会被 `received non-text message` 跳过。
- [x] 新增文件消息测试，覆盖文件下载、保存、本地路径进入 Agent 消息。
- [x] 新增异常测试，覆盖文件缺少 media 时不调用 Agent 并返回失败提示。
- [x] 增加入站文件提取 `extractFile`。
- [x] 增加入站文件保存：优先保存到 `save_dir`，未配置时保存到默认 workspace。
- [x] 生成 Agent 可读消息：用户原始文字 + 文件名 + 本地路径。
- [x] 运行全量测试并补充 review 小结。

### Review 小结

已完成微信文件输入最小闭环。文件消息会下载保存到 `save_dir`，未配置时保存到默认 workspace；随后把文件名和本地路径交给 Agent。文件缺少下载信息时不会调用 Agent，会返回“文件保存失败”。验证命令：`go test -count=1 ./... && git diff --check`，结果通过。

## Codex 本机手动切号刷新清单

### 目标

解决用户在本机手动切换 Codex 登录账户后，微信侧仍复用旧 Codex app-server 进程和旧 thread 的问题。

### 执行任务

- [x] 根据日志确认现象：微信请求复用旧 `pid` 和旧 thread，错误来自 `402 Payment Required / deactivated_workspace`。
- [x] 新增 `/sw reload` 测试，要求只刷新 WeClaw 内部 Codex Agent，不执行外部切号脚本。
- [x] 新增 Codex stderr 兜底测试，未知 error 事件要带上 stderr 里的账号态错误。
- [x] 新增账号态错误清理测试，遇到 `deactivated_workspace` 后要移除旧 thread。
- [x] 实现 `/sw reload`、`/sw refresh`、`/sw restart`。
- [x] 遇到 Codex 账号态错误时清理旧 thread 并停止旧进程，下一次请求使用当前本机登录状态。
- [x] 运行定向测试并补充 review 小结。

### Review 小结

已完成本机手动切号后的刷新入口和账号态错误自愈。`/sw reload` 可在不切号的情况下刷新 WeClaw 内部 Codex Agent；如果 Codex 返回 `402 Payment Required`、`deactivated_workspace` 或额度类账号态错误，会清理旧 thread 并停止旧进程，避免后续微信请求继续使用失效登录态。验证命令：`go test ./messaging -run 'TestHandleSwitchCommand_ReloadRefreshesCodexAgentWithoutRunningScript|TestHandleSwitchCommand_RestartsRunningCodexAgentAfterSwitch' -count=1` 与 `go test ./agent -run 'TestHandleCodexErrorUsesStderrWhenPayloadUnknown|TestACPAgentInvalidatesCodexRuntimeOnAuthStateError|TestFormatCodexErrorHandlesDeactivatedWorkspace|TestFormatCodexErrorHandlesRawMessage' -count=1`，结果通过。
