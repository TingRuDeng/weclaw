# Lessons

## 2026-07-12 飞书即时任务卡片终态

- 触发条件：飞书需要在 Agent 首个进度事件前给出可见反馈，并把最终回答收敛到同一张卡片。
- 规则：即时反馈与终态通知必须分离；任务登记后立即建卡，只有卡片成功写入终态后才能发送“请查看上方卡片”的简短通知。
- 失败边界：开卡失败只提示一次并继续任务；终态更新失败不得发送完成通知，必须回退完整普通文本。
- 附件边界：先校验 allowed roots 并发送媒体，再把发送结果投影到卡片正文；整行本地绝对路径不能留在终态卡片中。
- 并发边界：广播 worker 必须在自身 context 取消前完成 stream 终态，不能把 `finish` 延迟到结果接收端。

## 2026-07-12 Codex owner 跨重启恢复

- 触发条件：WeClaw 已通过 app-server 接管 Codex thread，随后服务重启并再次执行 `/cx switch`。
- 规则：`weclaw_runtime` 是进程内所有权，持久化时必须转换为已确认可恢复的 `persisted_only`；不能直接丢弃 conversation 到 thread 的 owner 证据。
- 安全边界：`desktop_live` 跨重启只能降级为 `desktop_disconnected`，普通断线不能产生 release evidence，避免与仍在运行的 Codex App 双写同一 thread。
- 验证方式：回归测试必须覆盖写盘、创建新 Agent、重启 app-server、`thread/resume` 原 thread，以及 Desktop live/disconnected 拒绝恢复。

## 2026-07-11 Codex App 跨进程控制提示

- 触发条件：用户从飞书或微信切换到 Codex App 独立进程正在执行的会话。
- 规则：切换反馈和 `/ps` 只展示任务、进度和结果回传，不主动提示“需在 Codex App 中操作”；只有用户实际发送 `/stop` 时才说明跨进程停止限制。
- 能力边界：`turn/interrupt` 只能控制当前 app-server 内存中已加载的 active turn；rollout 镜像不能把停止 WeClaw watcher 伪装成停止 Codex App 任务，也不能通过杀进程实现定向停止。
- 来源：2026-07-11 用户反馈切换会话时不需要操作提示，并询问飞书、微信远程停止能力。

## 2026-07-11 Codex 暂存消息自动续跑

- 触发条件：Codex 正在执行任务，用户发送第二条普通消息且未选择 `/guide`、`/cancel` 或 `/stop`。
- 规则：上一任务进入终态后必须自动执行暂存消息，不能再转成“待确认消息”要求用户回复“确认”。
- 实现约束：暂存项不能只保存文本，必须保留原消息的账号、会话、回复通道和客户端标识对应的延迟执行动作。
- 用户纠正：二次确认增加了无意义操作，任务队列应默认连续执行；上一任务报错也不能阻塞下一条，只有用户显式消费或撤回才停止续跑。

## 2026-07-10 Codex App 跨进程任务镜像

- 触发条件：Codex App 本地进程正在执行 turn，用户从飞书切换到同一 thread。
- 规则：不能用 WeClaw 自己的 app-server `thread/read` 判断另一个 Codex App 进程的 active 状态；进程间不共享 active 状态和通知流。
- 反例：`thread/resume` 成功后只查询 WeClaw app-server，返回非 active 就静默跳过，导致飞书没有任务、进度和最终结果反馈。
- 正确做法：跨进程任务以共享 rollout 的 `task_started`、`task_complete`、`turn_aborted` 为生命周期来源，并从初始文件偏移增量跟踪进度与最终结果。
- 来源：2026-07-10 用户反馈“Codex App 本地正在执行任务，飞书切换到该会话后没有反馈”。

## 2026-07-10 Codex 非致命 warning

- 触发条件：Codex app-server 发送 `warning`，或 WebSocket 断开后回退 HTTPS。
- 规则：`warning` 只用于展示非致命运行状态；turn 必须等待 `turn/completed` 的最终状态，不能由空 `error` 提前结束。
- 反例：收到空 `error` 后立即返回“Codex 返回未知错误”，导致稍后到达的 HTTPS 回退 warning 和成功终态无人接收。
- 正确做法：有明确内容的 error 保持失败；无有效详情的 error 只记录日志，继续等待 `turn/completed`，并把传输回退映射为简洁进度。
- 补充规则：stderr writer 保存的是最近日志，不保证属于当前 turn；只能用明确可识别的认证、额度等错误补足空 payload，普通 stderr 必须等待 `turn/completed` 佐证。

## 2026-04-28 微信绝对路径误判

- 微信消息中以 `/` 开头的内容不一定是命令，也可能是 macOS / Linux 绝对路径。
- Agent 命令解析只能接受独立 token，例如 `/codex 任务` 或 `@codex 任务`；不能按路径分隔符 `/` 连续拆 token。
- 新增命令解析规则时，必须补“绝对路径作为普通文本转发给默认 Agent”的回归测试。

## 2026-04-28 微信换行展示

- 微信气泡会弱化或折叠单个逻辑换行，命令回复和 Agent 最终回复都不能只依赖单个 `\n` 保证视觉换行。
- 微信出站文本应在发送层统一做展示格式化，把逻辑换行转换为空行分隔；不要只修某个命令渲染函数。
- 修改发送层换行策略时，必须同时覆盖普通回复和长回复分段测试。

## 2026-04-28 微信进度反馈

- 微信客户端已经有“对方正在输入”可视化提示时，默认不要再额外发送“收到”“处理中”“进展”等中间文字气泡。
- 进度文字适合作为显式 `summary` / `stream` / `debug` 模式，不适合作为默认体验。
- 改默认进度体验时，必须覆盖“默认不发文字、仍发 typing 状态”的回归测试。

## 2026-04-28 Codex 会话切换

- `/codex switch <threadId>` 不能只切 thread；如果该 thread 已记录在某个 workspace 下，也必须同步切换 Codex Agent 的 workspace。
- Codex 会话状态涉及 thread 和 cwd 两个状态源，修改其中一个时必须检查另一个是否需要同步。
- 新增 Codex 会话命令时，要覆盖“跨 workspace 历史 thread 切换”的回归测试。

## 2026-04-28 Codex 重启恢复

- 只持久化 workspace -> thread 列表不够，还必须持久化用户当前 active workspace；否则重启后会回到配置里的默认 cwd。
- Codex 普通聊天、`/codex switch`、`/codex new` 和 `/cwd` 都可能改变 active workspace，必须同步写入 session store。
- 重启恢复类问题必须用“新 Handler 加载同一个 session 文件”的测试覆盖。

## 2026-04-28 Codex 同会话并发

- 同一微信用户、同一 Codex Agent、同一 workspace 不能并发进入 Codex turn；否则本地 Codex 队列和 WeClaw 进度会话可能出现结果归属错乱。
- 串行化边界应包住进度会话、Agent 调用和最终回复，确保第二条任务不会在第一条任务完成前启动自己的 typing/progress 生命周期。
- 并发类 bug 必须补“第二条消息等待第一条完成”的回归测试，不能只验证单次调用成功。

## 2026-04-28 Codex thread 归属

- Codex thread 必须稳定归属于创建它的 workspace；不能因为当前 Handler cwd 不同就在 `recordCodexThread` 中把旧 thread 写到新 workspace。
- session store 写入 thread 时必须保证同一个 thread 只出现在一个 workspace 下，否则 `/codex switch` 会按错误 workspace 恢复。
- 已记录 thread 恢复失败时必须显式报错，不允许继续走 `thread/start` 静默新建会话。

## 2026-04-28 Codex 引导对话

- Codex 执行中收到第二条普通消息时不能直接排队，也不能直接发送；必须先暂存并让用户明确选择 `/guide` 或 `/cancel`。
- `/guide` 表示用户放弃第一条微信侧最终回复，只关心引导后的结果；实现时要取消第一条监听并抑制第一条最终回复。
- `/cancel` 在该语境下只撤回暂存消息，不取消正在执行的 Codex 任务。
- 微信消息入口可以按用户串行分发，但 Codex 长任务不能同步阻塞该入口；必须先登记 active task，再后台执行，保证运行中还能处理 `/guide` 和 `/cancel`。
- 用户不处理运行中的暂存消息时，任务结束后不能静默丢弃；应自动执行该消息，不再增加二次确认。
- 自动打开 Companion 可见终端时必须可关闭，并且命令参数要做 shell/AppleScript 双层转义，避免工作目录包含空格或单引号时启动失败。

## 2026-04-28 Codex 会话命令

- 会话列表必须带稳定编号，`/codex switch <编号>` 要使用与 `/codex ls` 相同的排序，避免用户复制长 threadId。
- 修改命令命名时必须同步更新 `isCodexSessionCommand`、帮助文本、命令处理分支和回归测试。
- 旧命令如果不再作为用户入口，就不要继续出现在 `/help`，防止微信侧形成两套说法。

## 2026-04-28 Codex 额度错误

- `usageLimitExceeded` 只是额度耗尽，不代表登录态或工作区失效，不能自动 Stop Codex 进程或清理 thread。
- 用户需要手动切换 Codex 账号时，WeClaw 必须保留当前进程和 thread 映射，避免切账号后 `/codex switch` 遇到已关闭 stdin。
- 只有 `deactivated_workspace` 这类真实工作区/登录态异常才允许触发 runtime invalidation。

## 2026-05-28 Codex Companion attach/detach

- Codex Companion 默认应弱绑定本地终端，保持微信 remote 可独立使用；本地可见终端只能通过 `/cx attach` 或显式 `auto_launch: true` 打开。
- `/cx detach` 只能断开当前可见 Companion 连接，不能清理后台 endpoint 或停止 WeClaw，否则微信 remote 会被误伤。
- 调整 Codex 会话命令时必须同步覆盖命令识别、Handler 分支、帮助文本和不支持该能力的 Agent 提示。
- `/cx attach` 打开的可见终端不能只停在 `weclaw companion` 等待态；Companion 握手成功后必须立即启动对应 CLI 可见运行时，避免用户误判为卡死。
- Codex App 当前公开入口是 `codex app <workspace>`，未确认可 deep link 到指定 thread；App 接手命令应先打开当前 workspace 并回显 threadID，而不是假装能精确选中会话。
- Codex 的最终产品语义是 remote-first：微信普通任务应走 app-server，不依赖本地 Companion；`/cx attach` 应使用当前 thread 执行本地 `codex resume`，而不是重新打开一个独立 bridge 会话。
- App 和 CLI 是两种不同接手入口：用户可见命令应使用 `/cx app` 和 `/cx cli` 明确区分；`/cx attach` 只作为兼容入口，避免形成两套主要说法。
- Codex App 不可用时不要静默降级到 CLI；必须暴露 App 打开失败原因，并提示用户显式发送 `/cx cli` 接手当前 thread。
- 本地 Terminal / Codex App 是临时接手入口，不是微信 remote 的权威状态源；状态查询只记录 WeClaw 最近成功打开动作，不实时同步手动关闭，也不自动关闭本地窗口。
- WeClaw 主帮助只展示当前推荐路径；`/sw`、Companion、实时进度和主动推送 API 这类高级能力可保留，但不要继续挤进默认 `/help`，避免产品语义变散。
- 微信里的列表操作优先支持上下文短命令，例如 `/cx 0` 和 `/cx ..`；长命令保留为兼容和精确入口，但不应要求用户日常重复输入。
- 默认 `/help` 应是一屏操作卡，只回答“现在该发什么”；`/guide`、`/cancel` 这类情境命令应由运行中提示承载，避免用户在主帮助里提前理解低频状态机。
- 精简主帮助后，二级帮助必须补足命令说明；不能让 `/cx help` 只列裸命令，否则用户仍然不知道每条命令该何时使用。
- `/cx ls` 合并本机 Codex 历史 session 时必须过滤已不存在的 `cwd`；Codex App 不展示的旧 worktree 或临时目录不应继续污染微信工作空间列表。
- 清理 Codex workspace 必须由 `/cx clean` 这类显式命令触发，只删除 WeClaw 自己的持久化记录，不应删除 `~/.codex/sessions` 或 Codex App 历史文件。
- 命令命名默认只保留一个主入口；不要同时提供 `info/status`、`quota/usage` 这类记忆成本接近的别名，除非有明确兼容理由。
- 状态类命令不能直接展示空字段；当模型、账号或入口状态沿用默认值时，必须用明确占位文案说明默认语义。
- 列表选择命令应减少二次输入：如果用户进入的工作空间只有一个可切换会话，应自动切换；没有真实会话时应显式创建新会话草稿。

## 2026-07-03 WeClaw 后台进程重启

- 触发条件：排查运行中 WeClaw 服务并准备重启时。
- 规则：不能只依赖 `weclaw status` 或 pid 文件判断服务状态，必须同时核对 `ps`、18011 端口和最新日志时间戳。
- 反例：pid 文件指向已不存在或半启动的进程时继续执行 `weclaw restart`，会让 stop/start 卡在系统态，导致服务没有恢复。
- 正确做法：先用 `ps` 确认 pid 是否真实存在，再用 `lsof -nP -iTCP:18011` 和日志确认服务可用；pid 文件陈旧时先删除 pid 文件，再单独启动服务并复核端口。
- 来源：2026-07-03 飞书审批修复部署时，旧 pid 文件和 macOS `U` 状态进程导致 `restart/status/start` 均未完成。

## 2026-07-03 飞书群聊审批边界

- 触发条件：飞书群聊任务触发 Codex 审批时。
- 规则：审批卡片必须只发送给任务发起人，并在回调写入幂等记录前校验点击者是发起人。
- 反例：把审批卡片发到群聊后，仅用卡片级 `approval_key` 幂等，非发起人先点击会耗掉真正发起人的审批。
- 正确做法：审批按钮 payload 携带 `approval_owner`，Replier 按 owner 投递审批，Adapter 在记录审批动作前拒绝非 owner 点击。
- 来源：2026-07-03 用户确认“飞书审批在群里不应该给审批，应该只给指定的用户进行审批”。

## 2026-07-03 WeClaw 本机更新通道

- 触发条件：发布新版本后需要更新本机 `weclaw` 命令或重启本机服务时。
- 规则：本机安装必须使用 GitHub Release 资产和 `weclaw update` 通道；不能用本地 `go build` 产物直接覆盖 PATH 中的 `weclaw`。
- 反例：把 `/tmp/weclaw` 或仓库本地构建产物 `cp` 到 `/usr/local/bin/weclaw`，会绕过 Release 校验，并可能让 macOS 在 `_dyld_start` 阶段卡住，导致 `weclaw update/version/start` 被 killed 或挂起。
- 正确做法：先发布 Release，再运行 `weclaw update`；如果安装器本身已损坏，只允许下载 Release 资产和 `checksums.txt`，校验哈希后做一次引导修复，再继续用 `weclaw update` 验证。
- 来源：2026-07-03 用户指出“为什么每次都把本地包弄坏，而不是使用 weclaw update 命令更新本地包”。

## 2026-07-04 WeClaw 重启保护

- 触发条件：飞书或微信侧有 Codex 长任务执行时，需要执行 `weclaw restart` 或 `weclaw update --restart`。
- 规则：重启前必须通过服务进程的运行态接口确认没有 active task；有运行中任务时默认拒绝重启，除非用户显式传 `--force`。
- 反例：发布或清理进程时直接停止 WeClaw，会把飞书里的长任务取消成 `context canceled`，用户只能看到任务失败。
- 正确做法：服务进程通过本机 API 暴露实时 active task 数，CLI 重启前查询；已知进程存在但配置或 API 无法确认状态时默认阻断，手工恢复必须显式使用 `--force`，有任务时给出明确提示。
- 来源：2026-07-04 飞书任务失败排查发现发布/重启中断了 43 分钟的运行中任务。

## 2026-07-03 飞书按钮会话路由

- 触发条件：飞书里把 Codex 普通回复中的编号选项渲染成按钮，或把 Codex 权限请求渲染成审批按钮时。
- 规则：所有飞书按钮 payload 都必须透传 `feishu_session_key`；普通选择卡只表示继续对话，不等同于 Codex 权限审批。
- 反例：用户点击“确认计划”按钮后，因为按钮没有 session key，回调退化成裸用户 ID，被历史兼容逻辑归到 `wechat:<user>` 会话，导致像是新开会话。
- 正确做法：普通选择卡、审批卡和审批回调都保留原飞书会话路由；真正权限审批只来自 Codex 的 `requestApproval` 事件，缺少可选 decision 时不要弹飞书审批卡。
- 来源：2026-07-03 用户指出“这是要我输入确认，而不是弹审批卡片，WeClaw 是不是没区分 Codex 真正申请权限的方式”。

## 2026-07-06 飞书项目机器人会话模型

- 触发条件：需要多个项目并行接入 Codex，但不需要用回复串做同一机器人内的多项目隔离时。
- 规则：优先用“一个飞书机器人 = 一个项目入口”区分项目；飞书回复串 / 话题不应创建 WeClaw 子会话，`/cx new-thread` 不应作为内置命令。
- 反例：继续维护 `dm_thread` 或 group thread route，会把项目隔离、回复目标和 Codex 会话切换耦合到同一套复杂逻辑里。
- 正确做法：session key 只保留聊天维度；DM 为 `feishu:<tenant>:dm:<chatID>:<senderOpenID>`，群聊为 `feishu:<tenant>:group:<chatID>`，多项目并行交给多机器人入口。
- 来源：2026-07-06 用户确认“按单聊和群聊回复串隔离都移除的范围执行”。

## 2026-07-07 飞书多机器人用户身份

- 触发条件：同一个人同时使用多个飞书机器人，或把一个机器人里的 `allowed_users` 复制到另一个机器人。
- 规则：飞书 `open_id` 是应用级身份，同一员工在不同机器人应用下不同；多机器人白名单优先配置同开发商下稳定的 `union_id`。
- 反例：把 main 机器人的 `ou_...` 复制到 android 机器人，会导致新机器人收到事件后被白名单拒绝，主动发送也可能报 `open_id cross app`。
- 正确做法：入站消息保留 `open_id` 作为回复和会话主身份，同时把 `union_id/user_id` 作为授权别名；卡片回调也必须复用最近入站身份，避免消息可进但按钮被拒绝。
- 来源：2026-07-07 新飞书机器人有凭证且长连接成功，但用户发消息无响应，出站测试返回 `open_id cross app`。

## 2026-07-07 飞书机器人单聊入站权限

- 触发条件：新增飞书机器人时，出站消息成功，但用户在单聊里回复后 WeClaw 没有任何 `account=<app_id>` 入站日志。
- 规则：发送权限不等于接收权限；单聊入站必须开通 `im:message.p2p_msg:readonly`，并重新发布版本完成审批。
- 反例：只看到开放平台里 `POST /open-apis/im/v1/messages` 成功，就判断飞书长连接和机器人入站能力都正常。
- 正确做法：同时检查事件日志 `im.message.receive_v1`；若事件日志为空，优先补齐单聊入站、群聊 @、CardKit 和菜单相关最小权限，再发布正式版本。
- 来源：2026-07-07 新 `android` 飞书机器人长连接和出站 API 正常，但缺少单聊入站权限导致用户消息没有投递到 WeClaw。

## 2026-07-07 WeClaw 远程管理命令串行化

- 触发条件：在飞书或微信里连续执行 `/update` 和 `/restart`。
- 规则：会影响二进制或进程生命周期的管理命令必须串行执行；`/restart` 不能抢在 `/update` 完成前启动。
- 反例：每条管理命令各自起后台 goroutine，用户在更新下载或替换过程中立刻发送 `/restart`，会重新拉起旧二进制，看起来像远程重启没有生效。
- 正确做法：在 Handler 内对管理命令加同一实例串行锁，让后续 `/restart` 等前一个 `/update` 完整返回后再触发。
- 来源：2026-07-07 飞书执行 `/restart` 后仍运行旧版本，用户本地再次重启才更新生效。

## 2026-07-07 飞书任务流里的会话导航卡片

- 触发条件：同一个飞书会话里 Codex 任务运行中，用户又发送 `/cx ls`、`/cx cd` 或点击会话导航按钮。
- 规则：会生成飞书会话导航卡片的命令不能插入正在运行的任务流；任务运行中只回文本提示，避免卡在进度卡片和最终结果之间。
- 反例：任务卡片显示“任务已完成，正在发送最终结果”后，用户先前发送的 `/cx ls` 卡片夹在最终回答前面，视觉上像 WeClaw 自动切会话。
- 正确做法：飞书卡片入口先按普通消息路由查 active task；命中运行中任务时返回运行中提示，不发送导航卡片。
- 来源：2026-07-07 用户反馈“为什么任务卡片跟最终回答结果的中间出来一个会话切换的卡片”。

## 2026-07-07 飞书任务完成卡片文案

- 触发条件：飞书任务使用原生任务卡片展示进度，并把最终结果作为独立消息发送。
- 规则：完成卡片已有绿色“已完成”状态时，正文不能再重复“任务已完成”类文案。
- 反例：卡片顶部显示“已完成”，正文又显示“任务已完成，正在发送最终结果。”，用户会感到重复和噪音。
- 正确做法：最终结果独立发送时，进度流只更新为状态完成；飞书 done 且空正文的卡片不渲染主正文块。
- 来源：2026-07-07 用户反馈“任务结束后卡片已经有已完成提示，下面的任务已完成有点重复”。

## 2026-07-07 飞书远程重启失败回写

- 触发条件：飞书或微信里发送 `/restart`，但服务当前还有运行中的任务。
- 规则：会被运行中任务拦截的远程重启必须在消息层前置检查并直接回写平台，不能只让后台 `weclaw restart` 子进程把错误打到服务日志。
- 反例：`weclaw restart` 子进程输出“当前还有 1 个运行中的任务”到日志，但飞书页面没有任何反馈，用户不知道重启失败。
- 正确做法：`/restart` 不带 `--force` 时先检查 Handler active task 数；有任务时不启动重启子进程，直接提示等待完成、发送 `/stop` 或使用 `/restart --force`。
- 来源：2026-07-07 用户反馈“发送重启指令的时候报错了，但飞书页面没有任何反馈”。

## 2026-07-07 远程重启子进程进程组

- 触发条件：飞书或微信远程执行 `/restart`，由运行中的 WeClaw 服务派生本机 `weclaw restart` 子进程。
- 规则：负责重启的子进程必须脱离旧服务进程组；它的生命周期不能被旧服务 stop 超时后的进程组强杀影响。
- 反例：远程 `/restart` 子进程继承旧服务进程组，`stopAllWeclaw` 超时后执行 `kill -pid`，把还没来得及执行 `Starting weclaw...` 的重启子进程一起杀掉，最终服务停在未运行状态。
- 正确做法：构造远程 restart 命令时在 Unix 下设置独立 session / process group，本机手动 `weclaw restart` 与服务内派生的远程 restart 都要用进程、端口、API 和日志四项复核。
- 来源：2026-07-07 用户反馈“飞书里更新和重启 weclaw 有问题，现在手动重启也出问题了”。

## 2026-07-08 普通最终回复不要自动卡片化

- 触发条件：Agent 最终回复里包含“请选择”“1. ...”这类自然语言选项列表。
- 规则：普通最终回复必须按文本发送，不能仅凭文本形态自动生成飞书交互卡片。
- 反例：把 Codex/Claude 的普通回复通过正则识别成按钮卡片，用户会以为 WeClaw 又发起了审批或系统交互。
- 正确做法：只有审批、help、Codex 会话导航等显式业务入口才能调用 `AskChoices`；Agent 最终回复里的选择列表保持原文。
- 来源：2026-07-07 至 2026-07-08 用户多次反馈“怎么还是把普通消息弄成卡片了”。

## 2026-07-08 飞书只读事件订阅

- 触发条件：飞书开放平台新增事件订阅，或日志出现 `event type: ... not found handler`。
- 规则：只要应用订阅了事件，就必须在 SDK dispatcher 中注册处理器；对已读这类非业务事件也要显式空处理，不能让 SDK 按错误打印。
- 反例：只处理 `im.message.receive_v1` 和卡片回调，却订阅了 `im.message.message_read_v1`，用户阅读机器人消息后日志持续报错。
- 正确做法：业务事件进入统一消息流；只读、状态类事件注册 no-op 或结构化状态处理器，并补 dispatcher 级回归测试。
- 来源：2026-07-08 飞书日志反复出现 `im.message.message_read_v1, not found handler`。

## 2026-07-08 任务卡片实时状态来源

- 触发条件：Codex App / Claude 任务卡片要展示实时状态，或最终回复包含 Markdown 标题、列表、编号选项。
- 规则：任务卡片实时状态只能展示结构化进度或 Agent 计划/工具状态，不能把最终回答正文 delta 当作进度回填。
- 反例：把 `item/agentMessage/delta` 直接写入 progress，最终回复的 `## 交付说明` 或列表行会覆盖卡片状态，看起来像普通文本被渲染进卡片。
- 正确做法：Codex App 接入 `turn/plan/updated`、命令输出、文件变更等进度事件；最终回答只用于最终消息发送，并让渲染层优先选择 `进展：` 状态。
- 来源：2026-07-08 用户指出“又把普通文本回复渲染成卡片了”和“任务卡片实时状态看着不行”。

## 2026-07-08 飞书 admin_users 身份

- 触发条件：判断飞书用户是否可执行 `/update`、`/restart`、`/feishu users` 等管理命令。
- 规则：飞书 `admin_users` 只允许使用 `union_id`；不保留 `open_id` 或 `user_id` 兼容。
- 反例：把某个机器人里的 `ou_...` 写进 `admin_users`，换另一个机器人后同一个人不再是管理员，或误以为多应用身份通用。
- 正确做法：飞书管理命令只读取 `feishu_union_id` 或 `on_` 形态的 union_id 别名；`/feishu users approve --admin` 在缺少 union_id 时必须拒绝写入。

## 2026-07-08 飞书用户列表友好显示

- 触发条件：`weclaw feishu users pending/list` 需要给管理员展示可识别的人名。
- 规则：用户列表可以通过飞书通讯录接口补全姓名，但姓名只能作为展示增强，权限判断和授权写入仍必须使用 `union_id/user_id/open_id`。
- 反例：只显示 `union_id/open_id`，管理员无法判断是谁；或者让通讯录姓名查询失败影响授权流程。
- 正确做法：展示层保留稳定 ID，同时优先显示手动备注名，其次显示通讯录姓名；通讯录查询失败时提示 `weclaw feishu users rename <id> <显示名>` 手动补全，不阻塞后续授权。
- 来源：2026-07-08 用户确认“那不用兼容，直接使用 union_id”。

## 2026-07-08 飞书授权码流程

- 触发条件：飞书新用户未在 `allowed_users` 中，第一次给机器人发消息。
- 规则：未授权提示应给短期授权码，管理员用 `approve-code` 授权；授权码只映射到已发现的稳定身份，授权成功后必须清空，过期后必须拒绝。
- 反例：让管理员复制长 `union_id/open_id`，或让授权码长期有效、可重复使用。
- 正确做法：拒绝提示显示 `授权码: xxxxxx`；管理员执行 `weclaw feishu users approve-code <code> [--admin] [--name <显示名>]` 或 `/feishu users approve-code <code>`；姓名只作为展示字段，不参与权限匹配。

## 2026-07-08 飞书首次管理员初始化

- 触发条件：`admin_users` 为空时，需要把第一个飞书管理员加入远程管理白名单。
- 规则：首次管理员初始化不能依赖飞书内 `/feishu users approve --admin`，因为该命令本身需要管理员权限；必须提供本地 CLI 写配置路径。
- 反例：只支持飞书聊天命令写 `admin_users`，导致新安装环境只能手动编辑 `config.json`。
- 正确做法：本地 `weclaw feishu users approve <union_id> --admin` 复用同一套授权逻辑，写入 `allowed_users` 和顶层 `admin_users`。
- 来源：2026-07-08 用户指出“一开始就没配置管理员，那不就只能改配置文件了吗”。

## 2026-07-08 飞书-only 启动不依赖微信

- 触发条件：新用户只通过 `weclaw feishu add` 配置飞书，然后执行 `weclaw start`。
- 规则：只要飞书平台已启用且微信未显式启用，启动不能要求微信登录；微信默认启用只适用于没有配置飞书的旧默认场景。
- 反例：飞书 bot 已配置完成，但 `weclaw start` 因没有微信账号而弹出微信扫码，导致用户以为飞书配置无效。
- 正确做法：`wechatEnabled` 先尊重 `platforms.wechat.enabled`；未配置时，如果 `platforms.feishu.enabled=true`，则默认不启用微信。
- 来源：2026-07-08 用户指出“没登录微信不应该影响服务启动”。

## 2026-07-08 飞书身份授权输出

- 触发条件：用户执行 `weclaw feishu users pending/list` 准备授权飞书用户。
- 规则：输出必须直接给出可复制的授权命令，并把机器人 app_id 显示成可读标签，避免用户再反查配置。
- 反例：只输出 `机器人: cli_xxx` 和身份字段，用户不知道执行哪条命令、也分不清不同 bot。
- 正确做法：每条身份记录显示 `weclaw feishu users approve <id>` 和 `--admin` 示例；已配置 bot 显示为 `展示名 (name, app_id)`。
- 来源：2026-07-08 用户指出 pending 输出缺少授权提示且机器人显示 id 没法区分。

## 2026-07-08 飞书任务卡片进度替换

- 触发条件：任务卡片实时进度展示“进展：...”状态。
- 规则：任务卡片进度是当前状态，不是增量正文；更新时必须替换主内容，不能调用会追加内容的 CardKit 流式正文接口。
- 反例：把相同的 `进展：Codex 正在执行命令并产生输出。` 多次传给 `StreamContent`，飞书卡片会把它拼成一长段重复文本。
- 正确做法：任务卡片实时进度使用 registry 快照重建卡片并 `UpdateCard`，保留审批记录和单调 sequence；非任务流才使用流式追加接口。
- 来源：2026-07-08 用户截图反馈“任务卡片的实时进度有问题”。

## 2026-07-09 Codex App 实时进度可读行

- 触发条件：用户要求任务卡片展示 Codex App 实时输出，尤其是“类似 tail -f log，只输出最新一行”。
- 规则：实时进度不是原始 stdout 的最后一行，而是 Codex App 风格的当前动作摘要；必须维护 turn 内状态，按工具、文件、计划、文本生成聚合成稳定的一到两行。
- 反例：把 `item/commandExecution/outputDelta` 或 patch 原始 `+...` 行直接显示到卡片，信息噪声大；只做无状态字符串匹配，会在有命令输出、文件变更、文本 delta 混杂时丢失当前动作。
- 正确做法：命令显示 `运行 <command>` 并把命令输出最新有效行作为次要详情；文件事件显示 `修改/新增/删除 <path>` 并计数去重；计划显示当前步骤；只有普通文本 delta 时显示 `Codex 正在生成回复`；卡片主行展示当前动作，次行展示输出预览或完成计数。
- 来源：2026-07-09 用户多次纠正“跟 Codex App 本地显示的实时进度看着不一样”，并要求联网参考其他开源实现。

## 2026-07-09 进程存在性判断

- 触发条件：`weclaw status`、`restart`、`update --restart` 或 stop 逻辑通过 `Signal(0)` 判断 pid 文件里的进程是否存在。
- 规则：`Signal(0)` 返回 `EPERM` 表示进程存在但当前上下文无权探测，不能当成过期 pid 文件。
- 反例：沙箱或受限权限下 `processExists` 把 `operation not permitted` 当成不存在，导致 `weclaw status` 显示“未运行（存在过期 pid 文件）”，但实际 pid 仍是 `/Users/dengtingru/.local/bin/weclaw start -f`。
- 正确做法：进程存在性判断应把 `nil` 和 `syscall.EPERM` 都视为存在；只有明确 `ESRCH` 或其他不存在错误才视为不存在。
- 来源：2026-07-09 用户纠正“但 weclaw 在运行中，是通过飞书重启的”。

## 2026-07-10 飞书卡片权限身份一致性

- 触发条件：飞书管理员通过 `union_id/user_id` 别名命中 `admin_users`，再点击工作空间导航卡片。
- 规则：卡片列表渲染和按钮回调执行必须透传同一个 `isAdminMessage` 结果，不能在回调阶段只按 `open_id` 重新判断。
- 反例：卡片按管理员权限显示完整工作空间，点击时按普通用户权限重新过滤，导致原编号提示不存在。
- 正确做法：飞书命令入口把基于主 ID 与全部别名计算出的管理员结果写入路由请求，后续列表、编号解析和工作空间限制统一复用。
- 来源：2026-07-10 用户反馈“在飞书里点击切换报错工作空间编号不存在”。

## 2026-07-11 实时任务终态发布顺序

- 触发条件：多个观察源竞争 Codex Desktop/rollout 终态，并异步发送最终回复、移除 active task、提升 pending。
- 规则：先原子认领终态，再发送最终回复，最后从 active registry 移除任务并提升 pending；“任务已消失”必须意味着最终回复写入已经结束。
- 反例：认领终态时立即删除 active task，再异步写回复；状态查询或测试看到任务结束后读取回复，会与写入并发，pending 也可能先于最终回复执行。
- 正确做法：把终态处理拆成 claim、publish、finish 三阶段；只有 claim 获胜者可以 publish 和 finish，其他观察源不得发送最终消息或执行 pending。
- 来源：2026-07-11 全仓 race 检测发现 external watcher 的完成状态早于最终回复写入。

## 2026-07-12 Codex Desktop 长会话终态归档

- 触发条件：通过 Desktop IPC 观察较长 Codex 会话，活动 turn 完成后需要向飞书回推结果并提升暂存消息。
- 规则：不能只比较相邻 revision 中同名 turn 的 `active -> terminal`；必须保留刚从活动区移除的 turn 指纹，跨过 `active -> absent -> terminal` 的归档窗口，且只在看到明确终态后结束任务。
- 反例：活动 turn 从 `tail:*:local:*` 实体移除时立即丢弃投影记忆，下一 revision 的 `turn:<id>` completed 实体会被当作全新历史，观察器永远收不到 completed，飞书暂存消息也不会自动执行。
- 正确做法：为消失的 active turn 保留短期 tombstone；终态实体出现时以同一 turn ID 恢复差分、发送一次终态并清除 tombstone；中间缺失 revision 不得误报完成。
- 来源：2026-07-12 实机快照确认 turn `019f5394-8c46-7a21-b5a3-16fa89fb22df` 已 completed，但 WeClaw 仍显示 `active_tasks=1`，飞书未收到自动结果。

## 2026-07-12 Codex Desktop 观察器状态复核

- 触发条件：通过实时事件等待外部 Desktop turn 完成，并在终态后发送结果或提升暂存消息。
- 规则：观察器必须记录开始时的 turn ID，并采用“实时事件 + 周期状态复核”；终态事件只能加速收尾，不能成为唯一收尾条件。
- 反例：`WatchCodexThread` 注册 channel 后无限等待 completed；状态缓存已经显示原 turn 完成或出现新 turn，观察器仍不退出，`active_tasks` 和暂存消息永久滞留。
- 正确做法：实时事件按目标 turn ID 过滤；周期读取权威线程状态，原 turn 不再 active 时使用已聚合文本或最近助手结果收尾；后续 turn 的事件不得被旧观察器消费。
- 来源：2026-07-12 首次终态归档修复部署后，飞书仍未收到自动结果且 `/api/runtime` 持续返回 `active_tasks=1`。

## 2026-07-12 Codex Desktop revision 屏障

- 触发条件：连接 Desktop IPC 后加载目标 thread，或根据加载结果决定当前 active turn。
- 规则：`thread-follower-load-complete-history` 成功响应只表示 Desktop 已发送目标 revision；调用方必须等待该连接代次的状态缓存实际达到响应 revision，才能读取状态或建立 watcher。
- 反例：初始化时 Desktop 同时广播多个已打开会话，旧目标 snapshot 先进入缓存；`LoadHistory` 收到成功响应后立即读取，错误地把上一个已完成 turn 当作当前 active turn。
- 正确做法：只投影 WeClaw 明确接管的 thread；解析 history 响应中的 revision，通过每 thread 唤醒机制等待缓存达到该 revision；状态事件长期静默时执行低频带屏障刷新。
- 来源：2026-07-12 实机对比 WeClaw 缓存 revision `4340` 与 Desktop 实时 revision `4533`，缓存 active turn 实际已 completed。

## 2026-07-12 飞书短任务进度卡收敛

- 触发条件：飞书使用原生流式进度卡，任务在初始展示延迟内完成，最终回复又要求单独发送文本。
- 规则：进度流只能在进度真正达到展示时机时创建；未展示任何进度的短任务直接发送最终回复，不得留下空的“已完成”卡片。排队接管必须继承当前账号已解析的进度配置。
- 反例：任务启动即创建流卡，或排队边界丢失账号级 `stream` 配置而回落到全局 typing；短任务结束后会同时留下 typing 完成卡、空进度完成卡和最终文本。
- 正确做法：延迟创建原生流卡；零延迟配置在非空进度到达时创建，正延迟配置只在进度实际发送时创建；排队外部任务直接传递调用入口已解析的 `ProgressConfig`；暂存状态只发送一行确认。
- 来源：2026-07-12 用户截图反馈飞书回复混乱，实机记录显示同一流程叠加暂存提示、空完成卡和最终文本。

## 2026-07-12 Codex Desktop 明确释放后的自动恢复

- 触发条件：飞书或微信会话仍保存 `desktop_live` 绑定，但 Desktop follower 对普通消息返回 `no-client-found`。
- 规则：`no-client-found` 是请求未被任何 Desktop 客户端处理的确定性 release 证据；应把 owner 原子转为 `persisted_only`，恢复同一 thread 到 WeClaw app-server，并只重试原消息一次。
- 反例：长期信任旧 `desktop_live` 绑定并直接返回错误；或者把断线、超时、交付状态未知也当成 release 自动重试，造成消息重复执行。
- 正确做法：只对 `ErrCodexDesktopNoClient` 执行 release、recover 和单次 app-server 重试；`ErrCodexDesktopDisconnected` 与 `ErrCodexDesktopDeliveryUnknown` 保持原错误和 owner，不做回退。
- 来源：2026-07-12 Android 飞书机器人发送普通消息后，日志立即返回 `没有 Codex Desktop 客户端可处理请求: no-client-found`。
