---
ai_summary:
  purpose: "提供 WeClaw 当前仓库形态、核心目录、文档路由、高风险区域和验证命令的精简上下文地图。"
  read_when:
    - "需要修改 WeClaw 功能、命令、平台 adapter、Agent runtime、配置或发布流程时。"
    - "排查飞书、微信、Codex、Claude、主动发送 API 或更新发布问题时。"
  source_of_truth:
    - "cmd/start.go"
    - "messaging/handler.go"
    - "messaging/terminal_outbox.go"
    - "observability/store.go"
    - "agent/codex_account.go"
    - "codexauth/store.go"
    - "platform/platform.go"
    - "platform/message.go"
    - "config/config.go"
    - "api/server.go"
    - "web/view.go"
    - "scripts/release.sh"
  verify_with:
    - "python3 scripts/validate_docs.py . --profile generic"
    - "git diff --check"
  stale_when:
    - "顶层模块职责、消息数据流、配置字段、命令列表、发布流程或验证命令变化。"
---

# WeClaw AI 上下文

## Project Snapshot

- 仓库类型：单一 Go 仓库，不是 coordination directory。
- Go 模块：`github.com/fastclaw-ai/weclaw`，以 `main.go` 和 `cmd/root.go` 进入 CLI。
- 产品定位：把微信个人号和飞书消息接入 AI Agent，业务层通过 `platform` 抽象隔离平台差异。
- 发布目标：`scripts/release.sh` 构建 `darwin/arm64`、`darwin/amd64`、`linux/arm64`、`linux/amd64`，本机安装必须走 `weclaw update`；发布门禁同时验证一键安装脚本，测试只能使用隔离的伪命令环境。`.github/workflows/release.yml` 只负责从 `main` 调用该权威脚本，不得复制一套较弱的测试、构建或 Release 逻辑。发布入口先统一配置持久化 `GOCACHE`：`WECLAW_GOCACHE` 优先，其次保留调用方显式导出的 `GOCACHE`；本机 Darwin 数据盘共享根目录存在时使用项目专属缓存，其他主机回退到 `go env GOCACHE`。

## Core Directories

- `cmd/`：CLI 命令、启动、停止、更新、重启保护、Companion 和发布相关入口；更新后重启与手动重启必须在停止旧服务前复用启动预检。
- `config/`：配置结构、默认值、Agent 探测、工作目录白名单、管理员白名单和 API 安全校验。
- `agent/`：统一 Agent 接口与各类 runtime；`codexauth/`：按 shared-host namespace 隔离的 OAuth profile、系统凭据库/显式文件后端、安全文件与跨进程事务锁。
- `platform/` 提供跨平台消息与回复抽象；`messaging/` 负责命令、会话、审批、结构化进展、任务状态、终态 outbox 和 Agent 调用。
- `observability/`：端到端 Trace 上下文、固定字段事件、轮转 JSONL 存储、受保护查询和 Codex 线协议脱敏录制。
- `feishu/` 负责飞书事件与交互；`wechat/`、`ilink/` 负责微信个人号接入、replier、token 和 monitor。
- `api/` 提供本机 HTTP API；`web/` 提供配置界面；`docs/`、`tasks/` 保存上下文、当前任务和长期 lessons。

## Documentation Map

- `AGENTS.md`：可移植代理入口，只放路由、约束和验证边界。
- `docs/README.md`：文档索引，说明 authority docs、任务记录和验证命令。
- `docs/AI_CONTEXT.md`：当前代码地图和高风险路径摘要。
- `README_CN.md`、`README.md`：产品级使用、配置和命令说明。
- `tasks/todo.md`：当前或正在执行的任务记录，不长期累积已完成流水账。
- `tasks/lessons.md`：发布、重启、飞书审批、Codex 会话等踩坑规则。

## Common Task Reading Paths

- 修改启动、停止、更新、远程管理命令或发布：先读 `cmd/start.go`、`cmd/update.go`、`cmd/restart_safety.go`、`messaging/admin_commands.go`、`scripts/release.sh`。
- 修改消息、任务、终态投递或 Trace：先读 `messaging/handler.go`、`messaging/task_state.go`、`messaging/terminal_outbox.go`、`messaging/trace.go`、`observability/`、`agent/acp_rpc.go`、`agent/acp_read_loop.go`、`api/server.go` 和 `cmd/trace.go`。
- 修改 Codex 或 Claude 行为：先读 `agent/acp_agent.go`、`agent/codex_app_server_host.go`、`agent/codex_host_supervisor.go`、`agent/codex_account.go`、`agent/codex_runtime_lease.go`、`codexauth/store.go`、`agent/acp_sessions.go`、`agent/acp_session_catalog.go`、`agent/claude_quota.go`、`agent/claude_quota_oauth.go`、`messaging/codex_sessions.go`、`messaging/codex_account_command.go`、`messaging/codex_remote_selection_store.go`、`messaging/codex_session_status.go`、`messaging/codex_browser.go`、`messaging/claude_sessions.go`、`messaging/claude_quota.go`、`messaging/agent_task.go`。
- 修改飞书体验：先读 `feishu/adapter.go`、`feishu/session_scope.go`、`feishu/choice.go`、`feishu/approval_panel.go`。
- 修改微信体验：先读 `wechat/`、`ilink/`、`messaging/progress.go`。
- 修改配置：先读 `config/config.go`、`config/detect.go`、`web/view.go` 和相关测试。
- 修改主动发送 API：先读 `api/server.go`、`platform/registry.go` 和 `api/server_test.go`。

## High-Risk Areas

- 飞书真实发送者身份和 session routing 必须分离；`feishu_session_key` 只用于会话路由。
- 飞书会话按聊天窗口聚合：DM 使用 `feishu:<tenant>:dm:<chatID>:<senderOpenID>`，群聊使用 `feishu:<tenant>:group:<chatID>`；回复串 / 话题不再生成独立 route。
- 飞书多项目入口通过 `platforms.feishu.bots[]` 配置多个机器人；每个 bot 的凭证按 `weclaw feishu login --name <bot>` 或 `weclaw feishu bootstrap --name <bot>` 保存，`app_secret` 不得写入 `config.json`。
- `weclaw feishu bootstrap` 只负责首次配置向导：保存凭证、更新 `bots[]`、提示可用 `lark-cli` 做权限/事件诊断；飞书运行时仍使用内置 SDK 长连接，不依赖 `lark-cli`。
- 飞书显式启用时必须配置非空 `bots[]`；缺失 bot 应在 `config.Validate` 阶段 fail-fast，不应等到平台启动阶段才失败。
- 飞书最小权限应覆盖单聊入站、群聊 @ 入站、发消息、资源、会话、CardKit 和机器人菜单：`im:message.p2p_msg:readonly`、`im:message.group_at_msg:readonly`、`im:message.group_at_msg.include_bot:readonly`、`im:message:send_as_bot`、`im:resource`、`im:chat`、`cardkit:card:read`、`cardkit:card:write`、`application:bot.basic_info:read`、`application:bot.menu:write`；`user` scopes 可为空。
- 飞书 bot 的 `allowed_users`、`default_agent` 和 `progress` 按 `app_id` 隔离；`allowed_users` 支持应用级 `open_id` 和同开发商下稳定的 `union_id`，多机器人优先配置 `union_id`；新增、删除 bot 或修改 `app_id` 属于平台拓扑变化，需要重启。
- 飞书未授权入站身份会在访问控制前写入 `~/.weclaw/feishu-identities.json`，但不会自动放行；管理员通过 `/feishu users pending/list/approve` 确认后才写入 `config.json`，本地只读查看使用 `weclaw feishu users pending/list`。
- `/progress` 从飞书入口触发时必须按当前 `account_id` 写入账号级配置，广播、Codex 会话切换和共享 host 任务 watcher 也必须读取账号级进度配置。Agent 到 messaging 使用 `agent.ProgressEvent` 传递 kind/state/id/path/sequence，旧字符串 Agent 只能在兼容边界包装；`Handler.activeTasks` 通过纯 reducer 保存唯一结构化视图，任务卡和 `/ps` 必须渲染同一 display text，终态水位线之后的旧 sequence 或晚到 watcher 不得覆盖终态。Codex 窗口切回仍在运行的任务时，只在 frontend binding 真实变化后于消息底部重建任务卡，并以 reducer 最新快照初始化；旧卡进入“已转移”且停止流式，后续进展和终态只能写当前卡，重复选择同一绑定不得重复建卡。独立任务卡可用时，会话切换结果只确认工作空间、模型配置和任务卡位置，不得重复展示任务正文、当前进展或终态回传说明；无任务卡的文本平台继续保留完整摘要。
- 飞书任务卡成功写入完成终态后不再补发成功消息；失败或停止只有在卡片终态写入成功后才发送简短通知。任务终态卡片和文本先写入 `~/.weclaw/state/terminal-outbox.json` 再触发网络调用；文件固定为 `0600`、同目录原子 rename 并 fsync，启动后自动续投。CardKit checkpoint 固定 UUID 与单调 sequence，飞书文本固定消息 UUID，微信文本固定分片 client_id；语义是平台幂等辅助下的 at-least-once，不承诺 exactly-once。附件与远程图片不进入 v1 outbox，仍按原有校验和 best-effort 路径发送。
- 每条入站消息创建一个 TraceID，排队续跑和广播 Agent 使用同根子 Span；message、task、turn、progress、reply 和 terminal outbox 只写固定字段事件到 `~/.weclaw/state/trace.jsonl`，路由键只落不可逆摘要，摘要和协议正文统一脱敏。文件固定 `0600`、目录固定 `0700`，单文件 10 MiB 后保留 3 个轮转备份。`weclaw trace` 在线只走真实 loopback 且继续校验 API token，服务存在但 API 不可达时失败关闭；离线仅只读本地 Trace。`WECLAW_CODEX_PROTOCOL_TRACE=1` 才记录 Codex 协议元数据，`WECLAW_CODEX_PROTOCOL_TRACE_PAYLOAD=1` 才额外记录有长度上限的脱敏 JSON；后者仍可能包含用户提示词和文件内容，只能临时诊断后关闭。
- 飞书 `/cx ls`、`/cc ls` 的工作空间按钮只携带服务端生成的 5 分钟一次性 opaque token，并绑定 Agent、真实点击者和窗口 route；旧版数字索引卡片必须提示过期，用户手工输入数字命令仍保持兼容。分页使用绑定机器人账号、点击者、窗口、Agent 和列表层级的 5 分钟服务端快照，翻页只读取快照；卡片回调去重优先使用飞书 `event_id`，缺失时使用每次卡片渲染生成的 revision，不能仅用“原消息 ID + 页码命令”判断重复点击。Codex 顶层工作空间列表只能读取项目头和排序，不能为了展示名称预加载每个项目的会话；会话查询必须推迟到用户进入具体工作空间后。
- `/api/send` 在同一平台存在多个可主动发送账号时必须要求 `account_id`，不能静默选择第一个账号。
- 飞书审批必须只发给任务发起人，并在回调写入幂等记录前校验点击者。
- 飞书推荐使用 7.22+ 悬浮菜单，菜单项通过“发送文字消息”触发命令，只保留 `/help`、状态/任务控制、Codex/Claude 列表与新建、模型/推理/确认模式等高频入口；可切换菜单受 3 个主菜单限制时移除“设置”主菜单。`/cancel` 等低频命令由飞书 `/help` 二级分类卡片承载，管理员分类只对管理员显示；已停用的 `/cc owner`、`/cc cli` 不得继续出现在帮助卡中。普通计划确认仍回复“确认”，Codex 运行中的暂存消息未被 `/guide`、`/cancel` 或 `/stop` 消费时会在上一任务结束后自动执行。
- 命令入口只保留当前主路径：远程更新使用 `/update`，Codex 会话使用 `/cx ...`，Claude 会话使用 `/cc ...`；不要重新引入 `/info`、`/clear`、`/upgrade`、`/codex ...` 会话入口、`/claude ...` 会话入口、`/cx open-app` 或 `/cx attach app` 这类兼容路由。`/cc <内容>` 同时保留为 Claude 消息别名，因此 Claude 会话命令只能在子命令和参数数量完整匹配时消费；带额外正文的 `/cc status ...`、`/cc new ...` 等输入必须落回 Claude 消息路径。窗口已显式选择但当前不可用的 Agent 必须失败关闭，禁止静默回退到平台或全局默认 Agent。
- Codex 运行拓扑是单一 app-server、多前端客户端。`agent/codex_app_server_host.go` 通过稳定 Unix socket 复用或启动唯一 host，并按上游标准 HTTP Upgrade 建立 WebSocket-over-UDS；禁止把该 socket 当裸 JSONL `net.Conn`。默认路径超过 `sockaddr_un` 限制时会稳定哈希到真实系统临时目录下的用户私有 `weclaw-<uid>` 目录；macOS 必须解析 `/tmp` 到 `/private/tmp`，因为 Codex 拒绝 socket 目录链中的软链接。host 生命周期独立于单次前端请求，普通客户端断开和恢复不得终止其他前端正在使用的 host。
- Codex OAuth profile 是 shared-host 级身份，不属于窗口或 thread。`codexauth/` 只接受 ChatGPT OAuth，索引按解析后的 `CODEX_HOME + socket` 生成 host ID，完整快照优先进入 Keychain/Secret Service；文件后端必须由本机 `--allow-file-store` 明确授权。目录/文件固定为 `0700/0600`，拒绝符号链接、异常 owner/权限，并按 gate → account lock → host lifecycle lock 获取切换事务锁。替换或删除 profile 时，旧 secret 删除失败必须先在索引中保留待清理引用并在后续事务重试，不能静默遗留 OAuth 凭据。
- `agent/codex_host_supervisor.go` 通过 socket 相邻的 `0600` 元数据验证 PID、UID、启动时间、进程组、命令与 generation；未知或遗留进程不得被终止。`agent/codex_account.go` 切换前同时检查 Handler task、全局 writer lease 和分页后的全部 archived/unarchived thread，停止真实受管 Host 前先持久化 `switching` journal，再投影目标认证，并以 `account/read` 和 `account/rateLimits/read` 验证。任一步失败都恢复旧认证与旧 Host；`switching` 或 `rollback_failed` 会在新进程首次使用 gate 时恢复为 failed，只有成功切换、完整回滚或停服后的显式离线 `use` 才能重新建立可写状态。在线 `save` 必须把 profile 索引与 Host 身份元数据作为一个带补偿的事务提交，旧 connection epoch 的 RPC/通知不得覆盖新 generation。
- `weclaw codex account ...` 在服务运行时只走真实 loopback 的本机 API；已有 `api_token` 继续校验。服务存在但 API 不可达时禁止直接改认证；服务停止时只允许离线 list/save/remove/use。`/cx account` 的列表和切换只对管理员私聊开放，普通用户和群聊只能看到当前脱敏标签；飞书确认 token 绑定机器人、操作者、route、profile 与 revision，5 分钟过期且重复点击幂等。
- Codex 窗口只持久化 frontend binding，不持有独占 writer owner。`messaging/codex_remote_selection_store.go` v4 只保存 route 到 workspace/thread 的绑定；v1-v3 owner/control 字段仅用于读取迁移，加载后必须丢弃并重写。多个飞书或微信窗口可同时绑定同一 thread，不能互相释放或覆盖绑定。
- Codex 的绑定入口包括 `/cx switch`、会话短编号、仅含一个会话的 `/cx cd`、飞书会话卡片、`/cx new` 和默认 Agent 为 Codex 时的全局 `/new`；这些入口最终复用 `messaging/codex_session_acquire.go:acquireCodexSessionWithBindingLocked`，新建入口先经过 `messaging/codex_session_new.go:createAndAcquireCodexSessionWithBindingLocked`。
- `messaging/codex_session_command_dispatch.go:prepareCodexSessionCommand` 在 route binding 锁内准备 `/cx` 命令；默认 Codex 的全局 `/new` 由 `messaging/default_session.go:resetDefaultCodexSessionForRoute` 持有相同 binding 执行锁。事务内部通过 `messaging/codex_session_locks.go:lockCodexSessionThreads` 按去重排序后的 thread ID 加锁，禁止反向获取 binding 锁。
- `messaging/codex_remote_selection_store.go:commitRemoteSelection` 使用 copy-on-write 候选副本，一次提交当前 route 的 selected thread 和 active workspace；持久化失败时不替换内存 maps，也不影响其他 route 的绑定。
- `messaging/codex_session_acquire.go:acquireCodexSessionWithBindingLocked` 先提交当前窗口 binding，再持久化窗口 Agent，最后把该 frontend conversation 映射到共享 app-server thread。Agent 选择落盘失败时只在 after-image 仍匹配时回滚；runtime 失败保留 durable binding 并明确报告通道暂不可用。
- `agent/codex_runtime_lease.go` 只把权威运行态 `weclaw_runtime` 视为可写，同一 thread 同时只允许一个 writer lease；route、窗口或旧 owner revision 不参与共享 host 写入授权。turn 接受后客户端断线必须保留 uncertain lease，只有 rollout 或重连后的 `thread/read` 确认同一 turn 终态才释放；晚到 probe、Desktop IPC 和旧 watcher 不能生成 conflict 或清除 binding。
- `/cx` 是 Codex 会话命令专用命名空间，未知子命令统一返回 `/cx help`；Codex 消息使用 `/codex <内容>` 或 `@cx <内容>`。`/cx owner` 及其旧参数已删除，binding、共享 host、writer 和任务状态统一由 `/cx status` 展示。`/cx app`、`/cx cli`、`/cx attach`、`/cx detach` 统一拒绝，旧 Codex Companion 配置无论 `auto_launch` 是否为 true 都迁移到 ACP 共享 host，禁止重新引入第二 app-server。`/model`、`/reasoning` 在窗口已绑定 Codex thread 时必须调用 app-server `thread/settings/update`，从同一 thread 的下一轮任务生效；无绑定时才修改显式新建 thread 的进程内默认值；`/cx model status` 只展示新建 thread 默认值，文案必须与当前 thread 配置明确区分。新 thread 默认值只进入 `thread/start`，`thread/resume` 和后续 `turn/start` 禁止重复注入共享默认值；当前 thread 配置从 `thread/start`/`thread/resume` 响应及 `thread/settings/updated` 通知缓存，旧 sequence 不能覆盖新配置，`thread/read` 不包含该配置。
- Claude 运行拓扑是单一进程驻留 ClaudeHost、多前端 binding：一个 WeClaw 服务只创建一个 Claude `ACPAgent` 和一个 `claude-agent-acp` 子进程，子进程生命周期由服务显式 `Stop` 或真实退出管理，不能继承触发启动的单次消息 context。标准 ACP `readLoop` 是 `Cmd.Wait` 的唯一所有者，自然 EOF、启动失败和显式 Stop 都必须等待同一个完成信号，禁止清空 `cmd` 后遗留 zombie 或重复 Wait。当前上游 adapter 使用 stdio ACP，因此前端通过 WeClaw 服务复用该 host；不要虚构或暴露未经实现的 Claude Unix socket 协议。
- `session/list` 是 Claude 目录事实源；`claudeSessionStore` v4 只持久化 route 到 workspace/session 的 binding、状态和单调 revision，v1-v3 的 `remote`、`local`、`unclaimed` control intent 加载后必须丢弃并重写。多个飞书或微信窗口可以绑定同一 session，不能互相释放或覆盖 binding；`ACPAgent.UseClaudeSession` 发现目标 session 已在当前 host generation 恢复时只提交 frontend conversation 映射，不得再次发送 `session/resume` 创建第二个 host-side session。
- Claude 的 `/cc switch`、`/cc new`、飞书会话卡片和默认 Claude 的全局 `/new` 都复用 binding-only acquire：先 CAS 提交当前 route binding，再持久化窗口 Agent，最后恢复共享 runtime。Agent 选择落盘失败只在 after-image 仍匹配时回滚；`session/resume` 或 ClaudeHost 失败保留新 binding 并记为 `resume_failed`，普通消息 fail-closed，但其他 route 的 binding 不受影响。重启加载后的有效 binding 统一为 `pending_resume`，首次真实使用恢复 runtime。
- Claude prompt 只按 `claudeSessionExecutionKey(sessionID)` 获取 session writer lease。`Handler.activeTasks` 的同一 session 槽位只允许一个 route 启动任务；同 route 最多排队一条续跑消息，其他 route 必须收到明确 busy 且不能把消息写入当前任务队列。任务登记前保存 binding session/revision 快照，prompt 前重验；route 自己切换或 `/cwd` 不能在其任务运行时改变 binding。Complete、Fail、Stop 和 Cancel 最终仍通过同一 active task 生命周期释放 lease。
- `/cc owner` 和 `/cc cli` 已停用，帮助与飞书卡片不得广告这两个入口；独立 `claude --resume` 会绕过共享 session writer lease，禁止重新引入 Claude CLI 聊天或本地交接。`AgentInfo.LocalCommand` 仅保留给 `/cc quota` 的短生命周期、无提示词额度查询回退：优先兼容 Claude Code 旧版 `Claude Code-credentials` Keychain 或 `CLAUDE_CONFIG_DIR/.credentials.json`（兼容 `claudeAiOauth`、`claude.ai_oauth`），只把内存中的 access token 发送到固定 Anthropic OAuth usage 地址；凭据不可读或请求失败时，原生进程只发送 `initialize`、`get_usage`。两条路径都不得记录/持久化 token、回显响应体、跟随重定向、扫描 transcript 或发送模型提示词。
- `/cc new` 后尚未进入 ACP 目录的空会话，只能由 `/cc ls` 从同 route 的 ready binding 暂态投影展示，不得进入 `/cc switch` 候选或绕过 `session/list` 校验。`/model`、`/reasoning` 在 ready session 上通过 ACP `session/set_config_option` 修改当前 session 并从下一轮任务生效；无 ready binding 时修改新 session 默认值，`/cc model status` 只展示默认值且同时显示 model 与 effort。飞书设置卡必须携带生成时的 session ID，并在 binding 锁内重验，旧卡不得写入新 session。
- Claude ACP 复用通用后台任务队列：每个活动任务最多暂存一条同 route 消息，失败后仍自动续跑；`/cancel` 撤回暂存，`/stop` 按当前窗口 Agent 停止，`/guide` 对 Claude 明确不支持。
- 微信 / 飞书显式绑定到共享 host 中正在运行的会话后，WeClaw 会读取 thread 状态、登记 active task observer，并在当前 turn 完成后回推结果。
- 运行中 Codex 长任务登记在 `Handler.activeTasks`；`restart` 和 `update --restart` 默认不能中断 active task。
- 微信 / 飞书远程管理命令由 `messaging/admin_commands.go` 执行 WeClaw 自身命令，不应进入 Codex / Claude；必须先校验顶层 `admin_users`，且管理员也必须在平台 `allowed_users` 内。飞书管理员身份判断必须同时检查 `open_id/user_id/union_id`。支持流式卡片的平台执行 `/update` 时必须在同一卡片内从版本检查收敛到“已是最新版本 / 更新成功 / 失败”终态；不支持流式卡片的平台降级为先回复后台受理、结束后另发最终结果。`/update` 摘要必须识别 CLI 当前使用的中文版本输出。
- 微信同一条入站消息触发多次回复时，`wechat.Replier` 必须为后续 `SendText` 生成新的 `client_id`，避免微信端把结果消息按重复消息去重。外部 `ILinkBotID` 写入凭据或 context token 路径前必须统一通过 `ilink.NormalizeAccountID` 编码，不能把 `/`、`\\`、绝对路径或 Unicode 原文直接交给 `filepath.Join`；旧版兼容映射发生文件名碰撞时，保存和加载都必须失败关闭。Web 扫码登录同一服务只允许一个 active poll，新登录必须取消并终态化旧会话，晚到回调不得保存旧凭据或覆盖新状态。
- Agent `permission_level` 省略时等同 `default`；显式配置只接受 `default`、`auto_review`、`full_access`，分别映射 Codex `approvalPolicy`、`sandboxMode` 与 `approvalsReviewer`；旧值必须 fail-fast。
- Web 配置保存必须保留 Agent 的 Codex 权限字段和共享 socket：`permission_level`、`approval_policy`、`approval_reviewer`、`sandbox_mode`、`app_server_socket`。
- `api_addr` 监听非 loopback 地址时必须配置 `api_token`。
- 发布后本机更新必须走 GitHub Release 资产和 `weclaw update` 校验，不要手工覆盖二进制。普通 `weclaw update` 在已是最新版时不得启动 Claude ACP 预检；实际安装新版本或显式 `update --restart` 才执行启动预检。
## Validation Commands

- quick: `python3 scripts/validate_docs.py . --profile generic`、`git diff --check`
- full: `go test ./... -count=1 -timeout 120s`
- full: `go test -race ./... -count=1 -timeout 180s`
- full: `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`、`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`
- release-side-effect: `scripts/release.sh --next-patch`

## Stale when

- 新增平台目录、Agent 类型、配置字段、会话存储、命令入口、API 端点，或飞书 session routing、审批卡片、Codex permission bridge、active task 生命周期改变。
- 正式发布资产矩阵、发布脚本验证命令，或 README 中的用户命令、配置示例、平台能力表更新。
