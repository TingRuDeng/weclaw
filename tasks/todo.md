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

## Codex workspace/thread 会话切换落地清单

### 目标

把 `codex-wechat` 的 Codex workspace/thread 会话模型移植到 WeClaw：保留多 Agent 能力，同时让 Codex 支持按 workspace 保存、查看、新建和切换 thread。

### 执行任务

- [x] 梳理 WeClaw 当前 Agent 会话 key、`/cwd`、`/codex` 路由和 ACP thread 持久化边界。
- [x] 新增 Handler 回归测试：Codex 消息使用 `user + agent + workspace` 作为会话 key，并把当前 thread 记录到 workspace 状态。
- [x] 新增命令测试：`/codex new` 清理当前 workspace thread，并进入新会话草稿。
- [x] 新增命令测试：`/codex switch <threadId>` 设置当前 workspace thread，并调用 ACP resume。
- [x] 新增命令测试：`/codex where` 和 `/codex workspace` 展示当前 workspace/thread。
- [x] 新增 ACP 测试：Codex app-server 支持外部设置、读取、清理 thread。
- [x] 实现 `agent.CodexThreadAgent` 可选接口，并在 `ACPAgent` 中实现。
- [x] 新增 `messaging/codex_sessions.go` 管理微信用户、Agent、workspace 到 thread 的状态。
- [x] 改造 Handler：识别 `/codex` 会话命令，Codex 聊天使用 workspace 会话 key。
- [x] 运行定向测试、全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex workspace/thread 会话切换的第一版落地。Codex 消息现在使用 `微信用户 + Agent + workspace` 组成独立会话 key；`/codex new` 会清理当前 workspace 的 thread 并进入新会话草稿；`/codex switch <threadId>` 会 resume 并切换到指定 thread；`/codex where` 和 `/codex workspace` 可查看当前状态。验证命令：`go test -count=1 ./... && git diff --check && go build -o weclaw .`，结果通过。

## Codex workspace/thread 列表持久化清单

### 目标

把 WeClaw 侧 `/codex workspace` 使用的 workspace/thread 列表持久化到本地文件，避免服务重启后历史 workspace 列表丢失。

### 执行任务

- [x] 新增持久化测试：thread 记录写入后，新 store 能从文件恢复。
- [x] 新增持久化测试：`/codex new` 的 pending new 状态能从文件恢复。
- [x] 为 `codexSessionStore` 增加 JSON 文件加载和保存。
- [x] 新增默认持久化路径 `~/.weclaw/codex-sessions.json`。
- [x] 在启动流程中为 Handler 配置 Codex session 持久化文件。
- [x] 运行定向测试、全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex workspace/thread 列表持久化。WeClaw 启动时会从 `~/.weclaw/codex-sessions.json` 加载 workspace/thread 列表，运行中更新 thread 或 pending new 状态时会写回该文件。验证命令：`go test -count=1 ./... && git diff --check && go build -o weclaw .`，结果通过。

## 微信命令回复换行展示修复清单

### 目标

修复 `/codex workspace` 等内置命令在微信气泡中单换行被折叠的问题，同时不影响 Agent 最终回复的原始换行。

### 执行任务

- [x] 新增命令回复换行回归测试，覆盖 Codex workspace、where、帮助、进度、状态和账号切换帮助。
- [x] 将内置命令回复统一为微信友好的空行分隔格式。
- [x] 保持 `SendTextReply` 原样发送最终文本，不做全局换行替换。
- [x] 运行定向测试、全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成微信内置命令回复换行修复。`/codex workspace`、`/codex where`、`/codex help`、`/progress`、`/info`、`/cwd` 和 `/sw` 脚本输出现在会使用空行分隔，避免微信气泡把多行内容挤成一段；`SendTextReply` 仍保持原样发送，不影响 Agent 最终回复里的真实换行。验证命令：`go test ./messaging -run 'TestCommandRepliesUseBlankLinesForWeChat|TestCodexWorkspaceRepliesUseBlankLinesForWeChat|TestHandleSwitchCommandFormatsScriptOutputForWeChat' -count=1` 与 `go test -count=1 ./... && git diff --check && go build -o weclaw .`，结果通过。

## 微信最终回复换行展示修复清单

### 目标

修复 Agent 最终回复在微信气泡中单换行被折叠的问题，让步骤、目的、执行结果等多行文本保持可读。

### 执行任务

- [x] 新增发送层回归测试，复现最终回复单换行没有转成微信可见空行的问题。
- [x] 在发送层增加微信展示格式化，将单换行转换为空行分隔。
- [x] 同步更新长回复分段测试，确保分段顺序和展示文本一致。
- [x] 运行定向测试、全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成微信最终回复换行展示修复。发送层会在 Markdown 转纯文本之后，把逻辑换行转换为空行分隔，解决微信气泡把步骤、目的、执行等多行结果压成一段的问题；长回复分段也基于展示文本进行，避免分段后再丢失可见换行。验证命令：`go test ./messaging -run TestSendTextReplyFormatsLineBreaksForWeChatDisplay -count=1`、`go test ./messaging -count=1` 与 `go test -count=1 ./... && git diff --check && go build -o weclaw .`，结果通过。

## 微信默认进度反馈静默化清单

### 目标

默认只保留微信“正在输入”状态和最终回复，不再发送“收到”“处理中”“进展”等中间文字气泡。

### 执行任务

- [x] 新增默认 typing 模式回归测试，确认默认不发送进度文字。
- [x] 将默认进度模式从 `summary` 改为 `typing`。
- [x] 将默认 `send_acceptance` 改为 `false`。
- [x] 保留 `summary`、`stream` 等显式进度模式能力。
- [x] 运行全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成微信默认进度反馈静默化。默认 `progress.mode` 从 `summary` 调整为 `typing`，默认 `send_acceptance` 调整为 `false`；长任务期间只保留微信 typing 状态，最终仍发送完整结果。显式切到 `summary` 或 `stream` 时，中间文字进度仍可用。验证命令：`go test -count=1 ./... && git diff --check && go build -o weclaw .`，结果通过。

## Codex 归档会话过滤与 cd 提示精简清单

### 目标

`/cx ls` 不展示已经归档的本机 Codex 会话；`/cx cd <工作空间>` 成功后只提示当前工作空间，去掉冗余的“已进入工作空间。”。

### 执行任务

- [x] 新增归档本机会话过滤测试。
- [x] 更新 `/cx cd` 提示测试，确认不再包含“已进入工作空间。”。
- [x] 实现 `archived_sessions` 线程过滤。
- [x] 精简 `/cx cd` 成功回复。
- [x] 运行定向测试、全量测试、diff 检查和构建。
- [x] 补充 review 小结。

### Review 小结

已完成 `/cx ls` 归档会话过滤和 `/cx cd <工作空间>` 回复精简。归档判断基于本机 Codex `archived_sessions` 目录中的 thread id，列表发现阶段直接跳过这些会话；进入工作空间后只返回 `工作空间: <名称>`。验证命令：`go test ./messaging -run 'TestDiscoverLocalCodexSessionsSkipsArchivedSessions|TestCodexCxCdWorkspaceThenLsListsSessionsWithoutThreadIDs' -count=1 -timeout 60s`、`go test ./messaging -count=1 -timeout 60s`、`go test -count=1 -timeout 60s ./...`、`git diff --check`、`go build -o weclaw .`，结果通过。

## ACP runtime 大消息与失效 stdin 崩溃修复清单

### 目标

修复 Codex app-server 输出大 JSON 行时触发 `bufio.Scanner: token too long`，以及读循环退出后继续向 nil stdin 写入导致 panic 的问题。

### 执行任务

- [x] 新增大 Codex 通知行读取测试。
- [x] 新增 nil stdin 写入不 panic 的测试。
- [x] 提升 ACP stdout 单行读取上限。
- [x] 统一 JSON-RPC 写入前的 runtime 状态检查。
- [x] 运行定向测试、全量测试、diff 检查和构建。
- [x] 补充 review 小结。

### Review 小结

已完成 ACP runtime 大消息与失效 stdin 崩溃修复。ACP stdout 单行读取上限从 4MB 提升到 64MB，覆盖 Codex MCP 启动状态这类大 JSON 通知；所有 JSON-RPC 写入统一经过 runtime 状态检查，读循环退出后会返回 `ACP runtime is not running`，不再对 nil stdin 执行 `fmt.Fprintf`。验证命令：`go test ./agent -run 'TestACPScannerReadsLargeCodexNotification|TestACPAgentCallReturnsErrorWhenRuntimeStdinMissing' -count=1 -timeout 60s`、`go test ./agent -count=1 -timeout 60s`、`go test -count=1 -timeout 60s ./...`、`git diff --check`、`go build -trimpath -ldflags="-s -w -X github.com/fastclaw-ai/weclaw/cmd.Version=v0.1.13" -o weclaw .`，结果通过。

## Review 安全与可靠性修复清单

### 目标

修复外部发送 API、远程媒体下载、ACP 子进程退出、Codex delta 路由、任务超时和进程重启 6 个 review 问题。

### 执行任务

- [x] 为 `/api/send` 增加 token 鉴权，并拒绝无 token 的非 loopback API 监听。
- [x] 为 URL 媒体下载增加 SSRF 校验、重定向校验和下载大小上限。
- [x] ACP 子进程退出时失败所有 pending RPC，并通知活跃 turn。
- [x] Codex delta 无法唯一路由时不再发送给第一个活跃 turn。
- [x] 让 `task_timeout_seconds` 对 Agent 调用真正生效。
- [x] 移除 `stopAllWeclaw` 的宽泛 `pkill -f`，只停止 pid 文件记录的目标进程。
- [x] 运行定向测试、全量测试、diff 检查和构建。
- [x] 补充 review 小结。

### Review 小结

已完成 6 个 review 问题修复：`/api/send` 增加 token 鉴权并禁止无 token 的非 loopback 监听；远程媒体下载增加协议、主机/IP、重定向和体积限制；ACP runtime 退出时会失败 pending RPC 与活跃 turn；Codex delta 无法唯一路由时不再任意 fallback；`task_timeout_seconds` 已覆盖默认、指定和广播 Agent 调用；后台停止逻辑移除 `pkill -f`，只按 pid 文件目标停止并确认退出后删除 pid 文件。验证命令：定向 `go test` 覆盖 6 组回归，`go test -count=1 -timeout 60s ./...`、`git diff --check`、`go build -o weclaw .`，结果均通过。

## Codex switch 同步 workspace 清单

### 目标

修复 `/codex switch <threadId>` 只切 thread、不切 workspace 的问题，避免切换历史 Codex 会话后还要额外执行 `/cwd`。

### 执行任务

- [x] 新增回归测试，复现已记录 thread 属于其他 workspace 时 `/codex switch` 仍使用当前 workspace 的问题。
- [x] 在 Codex session store 中支持按 thread 反查 workspace。
- [x] `/codex switch` 命中已记录 thread 时同步更新 Codex Agent cwd 和 Handler workspace 状态。
- [x] 运行全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 `/codex switch` 同步 workspace 修复。现在切换到已记录的 thread 时，会先从 Codex workspace/session 记录中反查该 thread 所属 workspace，再同步更新 Codex Agent 的 cwd 和 Handler 的 workspace 状态；如果 thread 未记录，则保留原有“当前 workspace 内切换”的行为。验证命令：`go test -count=1 ./... && git diff --check && go build -o weclaw .`，结果通过。

## Codex active workspace 重启恢复清单

### 目标

修复 WeClaw 重启后 Codex 回到配置默认 cwd，导致微信侧需要重新 `/codex switch` 的问题。

### 执行任务

- [x] 新增 active workspace 持久化测试。
- [x] 新增重启后按 active workspace 恢复 conversationID/thread 的回归测试。
- [x] 在 Codex session store 中持久化 `ActiveWorkspace`。
- [x] Codex 聊天、`/codex switch`、`/codex new`、`/cwd` 更新 active workspace。
- [x] 运行全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex active workspace 重启恢复。`codex-sessions.json` 现在会记录每个微信用户 + Codex Agent 的 active workspace；WeClaw 重启后，下一条 Codex 消息会先恢复 active workspace，再 resume 对应 thread，不需要重新 `/codex switch`。验证命令：`go test -count=1 ./... && git diff --check && go build -o weclaw .`，结果通过。

## Codex 同会话并发任务串行化清单

### 目标

修复本地 Codex 执行第一条任务期间收到第二条命令时，第二条结果覆盖/抢先返回，第一条进度状态残留的问题。

### 执行任务

- [x] 定位微信 Handler 到 ACP/Codex 的并发入口。
- [x] 新增同一 Codex 执行通道的串行化回归测试。
- [x] 在 Handler 中按用户、Agent 和 Codex workspace 串行化任务执行。
- [x] 验证默认进度生命周期仍由对应任务停止。
- [x] 运行全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex 同会话并发任务串行化。Handler 现在按用户、Agent 和 Codex workspace 生成执行 key，并把进度会话、Agent 调用和最终回复包在同一把锁内；同一执行通道的第二条消息会等待第一条完整结束后再进入 Codex，避免第一条结果被跳过和 typing/progress 生命周期残留。验证命令：`go test -count=1 ./...`、`git diff --check` 与 `go build -o weclaw .`，结果通过。

## Codex 引导对话与 thread 归属修复清单

### 目标

保留 Codex 执行中继续输入引导的能力，同时修复 thread 被错误绑定到其他 workspace 后导致同 workspace 新建会话的问题。

### 执行任务

- [x] 新增运行中第二条消息暂存测试。
- [x] 新增 `/guide` 发送暂存消息且抑制第一条最终回复测试。
- [x] 新增 `/cancel` 只撤回暂存消息测试。
- [x] 新增恢复旧 thread 失败时不静默新建会话测试。
- [x] 新增同一 thread 只能绑定一个 workspace 的测试。
- [x] 新增记录 thread 时保留已有 workspace 归属的测试。
- [x] 实现 Codex active task 与 pending guide 状态。
- [x] 修复 Codex thread 恢复失败继续新建会话的问题。
- [x] 修复 session store 中 thread/workspace 归属可被覆盖的问题。
- [x] 修复本机已错绑的 `codex-sessions.json`。
- [x] 运行全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex 引导对话与 thread 归属修复。Codex 执行中收到第二条普通消息时会先暂存并提示 `/guide` 或 `/cancel`；`/guide` 会取消第一条微信侧监听，只发送引导后的最终结果，`/cancel` 只撤回暂存消息。Codex session store 现在保证同一 thread 只能归属于一个 workspace；`recordCodexThread` 会保留已有 thread 归属，恢复旧 thread 失败时会直接报错，不再静默新建会话。本机 `codex-sessions.json` 已把 `019dd27f-441b-7282-9517-74b021a15b98` 重新绑定到 `/Volumes/Data/code/MyCode/card-manager-android`。验证命令：`go test -count=1 ./...`、`git diff --check` 与 `go build -o weclaw .`，结果通过。

## Codex 会话命令易用性调整清单

### 目标

将 Codex 会话命令改为更短的 `/codex ls` 和 `/codex whoami`，并支持按 `/codex ls` 中的编号切换 thread。

### 执行任务

- [x] 更新帮助文本测试，移除旧 `/codex where` 和 `/codex workspace`。
- [x] 更新 `/codex whoami` 与 `/codex ls` 命令测试。
- [x] 新增 `/codex switch <编号>` 回归测试。
- [x] 修改 Codex 会话命令解析。
- [x] 修改 `/codex ls` 输出，为每个 workspace/thread 增加 0 基编号。
- [x] 修改 `/codex switch`，支持编号或 threadId。
- [x] 运行全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex 会话命令易用性调整。`/codex where` 改为 `/codex whoami`，`/codex workspace` 改为 `/codex ls`；`/codex ls` 会按稳定排序输出 0 基编号，`/codex switch` 现在同时支持编号和 threadId。帮助文本已移除旧命令入口。验证命令：`go test -count=1 ./...`、`git diff --check` 与 `go build -o weclaw .`，结果通过。

## Codex 额度错误不刷新进程清单

### 目标

修复 `usageLimitExceeded` 被当作登录态异常处理，导致自动刷新 Codex 进程并破坏后续手动切账号和 thread resume 的问题。

### 执行任务

- [x] 新增额度耗尽不刷新进程、不清理 thread 映射的回归测试。
- [x] 保留 `deactivated_workspace` 自动刷新进程的既有测试。
- [x] 将 `usageLimitExceeded` 从 Codex auth state error 判断中移除。
- [x] 运行全量测试、diff 检查，并重新编译 `./weclaw`。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex 额度错误处理修复。`usageLimitExceeded` 现在只作为普通 turn error 返回，不会自动刷新 Codex 进程，也不会清理 thread 映射；`deactivated_workspace` 仍按真实工作区/登录态异常处理。验证命令：`go test -count=1 ./...`、`git diff --check` 与 `go build -o weclaw .`，结果通过。

## Codex 本机会话发现与绑定清单

### 目标

让微信里的 `/codex ls` 同时展示 WeClaw 已记录会话和本机 Codex 桌面端/CLI 会话，并复用 `/codex switch <编号>` 绑定切换。

### 执行任务

- [x] 新增本机 Codex 会话索引解析测试，只读取元数据字段。
- [x] 新增 `/codex ls` 合并本机会话测试，避免重复展示已记录 thread。
- [x] 新增 `/codex switch <编号>` 绑定本机会话测试，自动切换 workspace 并 resume thread。
- [x] 实现本机 Codex 会话元数据扫描器。
- [x] 接入 `/codex ls` 与编号切换。
- [x] 运行定向测试、全量测试、diff 检查和构建。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex 本机会话发现与绑定。`/codex ls` 现在会合并 WeClaw 已记录 thread 与本机 `~/.codex` 会话索引，并按 thread 去重；本机会话只读取 `session_index.jsonl` 与每个 session jsonl 的首个 `session_meta` 元数据，不读取对话正文。`/codex switch <编号>` 可直接绑定 `/codex ls` 中的本机会话编号，切换 Codex workspace 后 resume 对应 thread。验证命令：`go test ./messaging -run 'TestDiscoverLocalCodexSessionsReadsIndexAndSessionMeta|TestCodexLsIncludesLocalCodexSessionsAndDeduplicatesRecordedThread|TestHandleCodexSwitchCommandBindsLocalCodexSessionIndex' -count=1 -timeout 60s`、`go test ./messaging -count=1 -timeout 60s`、`go test ./messaging -cover -count=1 -timeout 60s`、`go test -count=1 -timeout 60s ./...`、`git diff --check` 与 `go build -o weclaw .`，结果通过；`messaging` 包整体覆盖率为 59.9%，低于 80% 的原因是历史包体量较大，本次新增行为已用三条定向回归覆盖。

## Codex 两级会话浏览清单

### 目标

让 `/cx ls` 先列工作空间，进入工作空间后再列该工作空间内的会话；会话列表只显示编号和名称，不暴露 thread id，并支持 `/cx cd ..` 返回工作空间列表层。

### 执行任务

- [x] 新增 `/cx ls` 工作空间列表测试，输出短名称并隐藏 thread id。
- [x] 新增 `/cx cd <编号|名称>` 进入工作空间测试，并同步 Codex cwd。
- [x] 新增 `/cx ls` 在工作空间内列会话测试，只显示编号和名称。
- [x] 新增 `/cx switch <编号>` 在当前工作空间切换会话测试。
- [x] 新增 `/cx cd ..` 返回工作空间列表测试，不改变 Codex cwd。
- [x] 新增 `/cx pwd` 和帮助文本测试。
- [x] 实现 Codex 浏览状态、工作空间聚合和会话过滤。
- [x] 接入 `/cx` 与 `/codex` 会话命令解析。
- [x] 运行定向测试、全量测试、diff 检查和构建。
- [x] 补充 review 小结。

### Review 小结

已完成 Codex 两级会话浏览。`/cx ls` 在默认层级只显示工作空间短名称；`/cx cd <编号|工作空间名>` 会进入工作空间并同步 Codex cwd；进入后 `/cx ls` 只显示当前工作空间内的会话编号和名称，不再暴露 thread id；`/cx switch <编号>` 按当前工作空间会话列表切换；`/cx cd ..` 仅返回工作空间列表层，不改变真实 Codex cwd；`/cx pwd` 可查看当前浏览层级。`/codex` 仍作为 `/cx` 的兼容写法。验证命令：`go test ./messaging -run 'TestCodexCxLsListsWorkspacesWithoutThreads|TestCodexCxCdWorkspaceThenLsListsSessionsWithoutThreadIDs|TestCodexCxSwitchUsesCurrentWorkspaceSessionIndex|TestCodexCxCdDotDotReturnsToWorkspaceListWithoutChangingCwd|TestCodexCxPwdShowsBrowseWorkspace' -count=1 -timeout 60s`、`go test ./messaging -count=1 -timeout 60s`、`go test -count=1 -timeout 60s ./...`、`git diff --check` 与 `go build -o weclaw .`，结果通过。
