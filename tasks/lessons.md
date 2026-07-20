# Lessons

## 2026-07-19 运行态诊断必须沿同一 Trace 串联协议与用户可见状态

- 触发条件：飞书任务卡长时间不更新、`/ps` 却有新进展，或 Codex app-server 出现断线、晚到 watcher、终态投递重试等跨层问题。
- 规则：每条入站消息创建一个 TraceID，任务、turn、结构化进展、卡片/文本回复和终态 outbox 都携带同一关联信息；任务卡和 `/ps` 必须由同一个纯 reducer 快照渲染，终态之后拒绝旧 sequence 和晚到事件。
- 反例：只打印原始协议日志，无法关联到平台消息和用户看到的卡片；或者卡片、`/ps`、watcher 各自维护一套字符串状态，导致同一任务显示互相矛盾。
- 正确做法：Trace 存储只接受固定字段，路由键只落不可逆摘要，摘要统一脱敏；文件读写和轮转基于受保护目录 fd 与 no-follow 操作，不能在 `Lstat` 后按路径直接打开。Codex 协议默认只记录 method/id/thread/turn 等元数据，正文必须显式启用、递归脱敏并限制长度，关联 ID 和关联表也必须有界。在线查询只允许真实 loopback 且复用 API token，服务存在但 API 不可达时失败关闭。
- 来源：吸收 OpenRelay 的事件关联与可观测性思路后，为 WeClaw 的消息、任务、共享 Host 和终态投递建立统一诊断链。

## 2026-07-19 Codex 账号是共享 Host 身份，不是窗口或 thread 状态

- 触发条件：本地 Codex 账号额度耗尽后手工登录另一个账号，但唯一 app-server 仍缓存旧 OAuth Token；飞书继续复用同一 workspace/thread 时返回旧账号 refresh 失败。
- 规则：账号 profile 只属于由 `CODEX_HOME + app-server socket` 定义的 shared host namespace。切换账号不得清理、重建或迁移 thread，也不得修改任何飞书/微信 frontend binding；下一条新消息才使用新账号，失败消息不自动重放。
- 安全边界：只接受 ChatGPT OAuth；索引只存脱敏身份、指纹和 secret 引用，完整快照优先存系统凭据库。文件降级必须由本机用户显式授权，并对目录、文件、owner、符号链接、原子写和跨进程锁执行严格校验。API、卡片、日志和 CLI 都不得输出凭据正文。
- 生命周期边界：锁顺序固定为运行时 gate → account lock → host lifecycle lock。停止 Host 前必须确认 Handler task、全部 writer lease 以及分页后的 archived/unarchived thread 均为空闲；active、unknown、旧 revision 或无法验证的受管进程都拒绝切换且不写认证。
- 回滚边界：目标认证写入后必须启动唯一 Host，并通过 `account/read` 与额度接口验证身份。任一步失败都停止失败 Host、恢复旧认证并重新验证；旧 Host 或账号索引未能完整恢复时 gate 保持 failed。连接 generation 切换必须与 wire dispatch 串行，旧 RPC 和晚到通知不能覆盖新运行态。
- 远程边界：一个主机只有一个全局活动账号。普通用户和群聊只能看到当前脱敏标签；只有管理员私聊可以列出或切换，飞书确认能力必须绑定机器人、操作者、route、目标 profile 和 revision，并具备 TTL 与重复点击幂等。

## 2026-07-19 Claude 必须是单一 ClaudeHost、多前端 binding

- 触发条件：每个飞书或微信窗口各自维护 Claude owner，并通过 `/cc cli` 把同一个 session 交给独立原生进程；窗口切换、ACP 恢复和 prompt 并发因此同时操作 session ownership，反复出现接管失败或潜在双写。
- 规则：一个 WeClaw 服务只运行一个进程驻留 ClaudeHost；窗口只保存 workspace/session binding，不持有独占 owner。多个前端可绑定同一 session，当前 host generation 中只执行一次有效 `session/resume`，真正 prompt 才按 session 获取唯一 writer lease。
- 运行边界：单次消息 context 取消、ACP 连接失败、`session/resume` 超时或子进程重启只能改变 host/runtime availability，不能清除 durable binding 或覆盖其他 route。重启后的 binding 进入 `pending_resume`，恢复失败进入 `resume_failed` 并 fail-closed。
- 并发边界：同 route 在活动任务后最多排队一条消息；绑定同一 session 的其他 route 必须收到明确 busy，不能把消息追加到现有任务，也不能通过重复 `session/resume` 建立第二个 host-side writer。绑定 CAS、session 有序锁和任务 lease 是不同层次，不能重新合并成 owner 状态机。
- 兼容边界：Claude 状态 v1-v3 的 `remote`、`local`、`unclaimed` control intent 加载后丢弃并重写为 v4 binding-only；`/cc owner`、`/cc cli` 停用，原生 `claude` 仅保留给 `/cc quota` 的无提示词短生命周期回退。上游 adapter 当前是 stdio ACP，未实现的 Claude Unix socket 不能写成现有能力。
- 本节取代下文所有 Claude owner-first、本地 CLI 交接和“多 binding 迁移为 unclaimed”规则；旧条目仅保留为历史故障背景。
- 来源：用户确认 Claude 按 Codex 的单一 host、多前端客户端方向重构，并允许直接推进当前无人使用阶段的架构收敛。

## 2026-07-18 API 收敛后必须删除失去调用方的兼容包装

- 触发条件：内部 API 增加新参数并迁移全部调用方后，旧签名只剩一个转发包装且生产代码、测试均不再使用。
- 规则：内部非公开 API 完成迁移后立即删除无调用包装，并在提交前运行与发布门禁一致的 staticcheck。
- 反例：保留“可能以后有用”的旧包装；单元测试和 vet 均通过，但正式发布到 staticcheck 才以 `U1000` 中断。
- 正确做法：迁移调用方后用 `rg` 核对旧符号引用，删除无调用包装，并在创建 tag 前执行 staticcheck，确保发布脚本不会承担首次发现死代码的职责。
- 来源：任务卡标题 API 加入工作空间快照后，发布门禁发现 `progressTaskTitleForAgent` 已无调用。

## 2026-07-18 Codex 会话命令与消息入口必须分离

- 触发条件：单一 app-server 架构已经取消窗口 owner，但 `/cx owner` 仍作为主命令展示，同时 `/cx` 还可能回退成 Codex 消息别名。
- 规则：`/cx` 只用于 Codex 会话命令；未知 `/cx` 子命令返回 `/cx help`。Codex 消息只使用 `/codex <内容>` 或 `@cx <内容>`，运行态统一由 `/cx status` 展示。
- 反例：只删除 `/cx owner` 的处理分支，让旧命令回退成发送给 Codex 的普通提示词 `owner`；或者同时保留 `/cx status` 与名不副实的 `/cx owner`。
- 正确做法：删除 `/cx owner` 的识别、处理、帮助和卡片入口，把 binding、共享 host、writer 与任务状态并入 `/cx status`，并用测试锁定 `/cx` 命名空间不会落入 Agent 消息路径。
- 来源：单一 app-server、多前端 binding 架构收敛后，用户确认不再保留 `/cx owner` 兼容入口。

## 2026-07-18 Codex Unix socket 必须执行 WebSocket Upgrade

- 触发条件：把原生 `codex app-server --listen unix://...` 改为共享 host，并让多个 WeClaw 客户端直接连接 Unix socket。
- 规则：Codex 的 Unix socket 承载标准 HTTP Upgrade 后的 WebSocket；每条 JSON-RPC 消息是一个 text frame。只有 stdio transport 才是逐行 JSONL，不能复用裸 `net.Conn` scanner/writer。
- 反例：测试 fake 使用 `net.Listen("unix") + bufio.Scanner + json.Encoder`，即使多客户端、锁和 lease 测试全绿，也无法发现真实 Codex 会以 `failed to upgrade control socket websocket connection` 关闭连接，最终在飞书表现为 `agent "codex" not available`。
- 正确做法：客户端通过 WebSocket dialer 的自定义 Unix `NetDialContext` 连接 `/rpc`；回归 fake 必须执行真实 HTTP Upgrade 并按 frame 收发。macOS 长路径回退还必须先把 `/tmp` 解析为真实的 `/private/tmp`，Codex 同样会拒绝 socket 目录链中的软链接。发布前至少对当前受支持的真实 Codex CLI 执行一次 `Start → initialize → Stop` 烟测，SQLite 初始化暂态仍走既有限次重试。
- 来源：`v0.1.204` 更新重启后，飞书 `/cx ls` 实机失败；日志与隔离实验确认 Codex CLI 0.144.5 拒绝裸 JSONL Unix 连接，改用 WebSocket-over-UDS 后真实 initialize 成功。

## 2026-07-18 Codex 必须是单一 app-server、多前端 binding

- 触发条件：飞书、微信、Codex Desktop bridge 和 Companion 分别维护 owner/runtime，窗口切换、Desktop 探测、断线与 writer lease 被混成同一状态机。
- 规则：生产 Codex 只运行一个共享 app-server；窗口只持久化 workspace/thread binding，不持有独占 writer owner。多个前端可绑定同一 thread，真正写入时只按 thread 获取单一 writer lease。
- 运行边界：socket 断开、启动失败或超时只影响 runtime availability，不能清除 binding、弹 owner 卡或写入 conflict；host 生命周期必须独立于触发启动的单次前端请求。
- 断线边界：turn 已提交后失去客户端观察流属于交付状态未知，writer lease 必须 fail-closed 保留；只有同一 turn 的 rollout 终态或重连后的权威 thread 快照才能释放，禁止用普通 error/defer 直接清 active 状态。
- 兼容边界：v1-v3 owner/control 仅用于读取迁移并丢弃；`/cx owner` 已删除，`/cx app|cli|attach|detach` 与 Codex Companion 第二 writer 必须拒绝。后续本地 UI 只能连接同一 host。
- 路径边界：Unix socket 默认路径过长时使用原路径稳定哈希落到当前用户私有短目录；显式超长路径直接失败，socket 与父目录必须校验类型、权限和 owner。
- 本节取代下文所有 Codex Desktop owner、选择即接管和 Codex Companion attach/detach 规则；Claude 当前规则以 2026-07-19 的单一 ClaudeHost 条目为准。

## 2026-07-17 Codex 首轮前跨重启恢复不能要求 rollout 文件

- 触发条件：`thread/start` 已创建并绑定新 thread，但尚未收到第一条用户消息；WeClaw 随后重启，用户重新选择该会话以恢复运行通道。
- 规则：普通已 materialize 会话的远程恢复仍必须前后校验稳定 rollout checkpoint；唯有 checkpoint 全空且 app-server 在 `thread/resume` 后明确返回“首条消息前未 materialize”时，才可把该协议结果作为“从未发生写入”的强证据恢复空会话。
- 反例：对所有 remote 恢复统一先要求 rollout 路径；新会话按协议尚未生成 rollout，因此每次重启后都会被误标为写入冲突，即使 owner、ACP mapping 和 thread 均正确。
- 正确做法：先区分“全空 checkpoint 候选”和非法/变化的 checkpoint；候选仅在 app-server 确认 pending first turn 后放行。若 thread/read 已有 turns、返回其他错误或 checkpoint 非空但无效，继续 fail-closed。回归测试必须同时覆盖空会话恢复成功和 materialized 会话无 checkpoint 仍拒绝。
- 来源：2026-07-17 `v0.1.194` 实机旧 owner 命令日志确认目标新 thread 的 binding 已提交，最终仅因 `Codex rollout checkpoint 缺失` 被标为写入冲突。

## 2026-07-17 Codex 展示目录不得清除远程会话绑定

- 触发条件：`/cx new` 已创建并接管 thread，但首条用户消息尚未让该 thread 写入 Codex App 展示目录；随后执行 `/cx status`、`/cx ls` 或其他读取会话目录的命令。
- 规则：持久化的 route binding 与 remote control intent 是当前窗口选择的权威状态；Codex App SQLite、会话索引等展示目录只能补充名称和列表，目录暂时看不到 thread 不能反向清空仍由该 route 远程持有的绑定。
- 反例：状态查询为显示新会话名称读取 App SQLite，因新 thread 的 `preview` 为空而得到空列表，随后把当前 `ThreadID` 当成失效缓存清空；下一条普通消息因此错误提示“没有有效的 Codex 会话”。
- 正确做法：清理展示目录中不可见的旧缓存前先核对 control intent；同一 binding 仍持有 remote owner 时保留 thread。回归测试覆盖 `/cx new → /cx status → 第一条普通消息`，同时保留“无 owner 的归档旧缓存仍会清除”的反向测试。
- 来源：2026-07-17 用户执行 `/cx new` 后查询状态，下一条普通消息仍提示重新选择或新建；日志与状态文件时间戳确认 `/cx status` 清空了刚创建的 thread 绑定。

## 2026-07-17 Codex 空会话不得误判为写入冲突

- 触发条件：`thread/start` 已成功返回新 thread，但该 thread 尚未收到第一条用户消息；随后会话接管或外部任务观察读取 `thread/read(includeTurns=true)`。
- 规则：Codex app-server 返回 `includeTurns is unavailable before first user message` 表示确定的空闲空会话，不是 runtime 故障，更不是 writer 冲突；只有 writer lease 重叠或不同 active turn 等实际写入证据才能进入 `CodexRuntimeConflict`。Desktop IPC 不可用只能影响旧通道清理或 runtime 可用性，不能污染新 thread。
- 反例：`/new` 成功创建并接管新 thread 后，外部任务观察把“尚未 materialize”当成读取失败，调用 `MarkCodexRuntimeConflict`，导致首条普通消息被永久阻断。
- 正确做法：在 Agent 协议边界把首条消息前的 turns 不可读归一化为空闲 `CodexThreadState`；回归测试必须覆盖 `ResetSession → HandoffCodexRuntime → 空会话读取 → 第一条 writer lease`，发布前再用真实 Codex app-server 验证 `thread/start → 空会话读取 → 第一条 turn`。
- 来源：2026-07-17 用户执行 `/new` 后立即看到“运行位置: 写入冲突”；真实日志确认新 thread 没有 active turn，协议级冒烟复现并验证修复。

## 2026-07-17 飞书分页快照与回调幂等必须分离

- 触发条件：用户在 `/cx ls` 或 `/cc ls` 卡片中反复点击上一页、下一页，重新进入之前访问过的页码。
- 规则：列表内容应在首次打开时生成短期服务端快照，翻页只读取快照；每次真实点击必须按飞书 `event_id` 区分，同一事件重投仍保持幂等。事件 ID 缺失时使用卡片渲染 revision 兜底。
- 反例：用“原消息 ID + `/cx page workspaces 2`”作为回调消息 ID；用户第二次进入第 2 页时会被消息去重拦截，业务层没有返回新卡，原卡遂被收纳成绿色“已完成：下一页”。每次翻页重新扫描 Codex/Claude 目录也会造成不必要查询和页序漂移。
- 正确做法：快照绑定机器人账号、真实点击者、窗口 binding、Agent、列表层级和工作空间，设置明确 TTL；导航按钮透传 opaque snapshot token，回调层透传该 token 并以事件 ID/revision 生成稳定且不冲突的消息 ID。
- 来源：2026-07-17 用户截图与 11:12 日志确认“第 2 页 → 第 1 页 → 再到第 2 页”后卡片变为“已完成：下一页”。

## 2026-07-16 Codex 会话索引扫描不能静默截断

- 触发条件：读取 Codex `session_index.jsonl` 这类追加式 JSONL 索引，并用其中的 `thread_name` 对齐 App 会话名称。
- 规则：不能依赖 `bufio.Scanner` 默认 64 KiB token 上限，也不能忽略 `scanner.Err()`；任一历史超长记录都不得让后续索引静默消失并触发错误兜底。
- 反例：前部一条超过 64 KiB 的会话名让扫描提前终止，后面的 App 重命名未进入 map，飞书遂回退显示 SQLite 中的首条消息标题，看起来像两端会话不是同一个。
- 正确做法：使用有明确单行上限、超限后能丢弃当前行并继续读取的 reader，检查并上报扫描错误；回归测试必须同时覆盖“超过 Scanner 默认上限”和“超过业务硬上限”的记录在前、目标 thread 在后，并验证仍使用目标 `thread_name`。
- 来源：2026-07-16 用户用飞书会话卡片截图纠正“第二层名称一致”的判断，现场复现索引第 56 行超限后第 390 行未被读取。

## 2026-07-16 更新命令的版本结果与预检必须分流

- 触发条件：本地或飞书执行更新命令，当前二进制已经是最新版本；或者 CLI 更新输出文案、语言发生变化。
- 规则：普通 `weclaw update` 在版本未变化时立即返回，不启动 Claude ACP 预检；显式 `update --restart` 仍保留预检和安全重启。IM 结果摘要必须覆盖 CLI 当前真实输出契约。
- 反例：最新版仍启动最长 35 秒的 ACP 探针；同时飞书摘要只识别旧英文 `Already up to date` / `Updated to`，把中文版本结果丢弃并误报成泛化的“更新完成”。
- 正确做法：先按版本差异决定是否下载和预检，再把远程命令明确表述为“后台受理”；最终回复从中文“已是最新版本 / 已更新到”中提取版本号，并保留旧英文兼容。
- 来源：2026-07-16 用户发现飞书 `/update` 看似瞬间完成，而本地 `weclaw update` 长时间等待。

## 2026-07-15 Codex 普通消息必须以持久化 owner 为准

- 触发条件：飞书或微信窗口已经成功选择并接管 Codex thread，后续普通消息启动前遇到 Desktop 探测超时、断线或 runtime unknown。
- 规则：owner tuple 和 revision 是写入授权事实源；普通消息只读取接管事务已建立的 runtime binding，不重复探测 Desktop，也不根据运行通道异常释放 owner 或要求用户重新选择。
- 失败边界：runtime 不可用时普通消息拒绝本次写入并保留 binding；显式重新选择遇到断线、重启后的 unknown 或旧 conflict 时，应校验 rollout 并恢复 WeClaw app-server。
- 反例：把 `context deadline exceeded`、ownership unknown 或 runtime unavailable 统一渲染成“交给当前远程窗口 / 交给 Codex Desktop”卡片，让用户误以为所有权自动释放。
- 来源：2026-07-15 飞书主机器人窗口在 22:45 已接管，22:53 普通消息因 Desktop IPC 超时再次弹出所有权选择。

## 2026-07-16 会话所有权提交与运行通道恢复必须分层

- 触发条件：微信或飞书窗口显式选择、新建或接管 Codex/Claude 会话，但 Desktop IPC、ACP resume 或外部任务 observer 暂时不可用。
- 权威边界：窗口选择、Agent 和唯一 remote owner 是持久化控制事实；runtime binding 只回答当前从哪里执行，不能反向撤销用户选择。只有本地持久化失败、跨远程窗口所有权冲突或活动远程任务冲突属于选择硬失败；Desktop 与 WeClaw 的 turn 可以并存。
- 一致性规则：先原子提交 binding/owner，再持久化窗口 Agent，最后同步 runtime；Agent 持久化失败用 after-image CAS 回滚 owner。Codex 显式接管遇到 Desktop 探测不确定时恢复 WeClaw，Desktop 的不同 turn 不得取消当前 writer lease；真正的 runtime/observer 失败仍保留 owner 并报告通道不可用。Claude 的 `resume_failed` 门禁保持不变。
- 释放规则：显式归还 Desktop/Local 也先提交 owner，再尽力同步运行通道；同步失败不得恢复远程 owner。
- 来源：2026-07-16 Android 飞书窗口反复切换 Codex/Claude 失败，用户确认以窗口绑定和显式释放为所有权事实源。

## 2026-07-14 Codex interrupted 不是可靠终态

- 触发条件：Codex app-server 返回 `turn/completed status=interrupted`，但共享 rollout 中同一 turn 仍继续产生进展。
- 规则：`interrupted` 必须保留 `threadID + turnID` 并交由 Messaging 核对；不能在 Agent 协议层直接映射为最终失败。
- 恢复边界：只允许接续同一 turn；明确 `turn_aborted`、目标 turn 被替换、rollout 读取失败或用户取消时才结束，禁止自动跟随新 turn。
- 性能边界：大型 rollout 首次只扫描一次历史，目标 turn 尚未落盘时应从 EOF 增量等待，不能按轮询间隔反复全量扫描。
- 来源：2026-07-13 飞书收到“Codex turn 已中断”后，本地同一 turn 仍持续执行并最终完成。

## 2026-07-13 飞书卡片会话切换完成语义

- 触发条件：飞书会话选择卡通过异步 goroutine 回放命令，后端切换需要 owner 探测、runtime 解析和持久化绑定。
- 规则：卡片回调返回前必须登记同窗口顺序屏障；后续普通消息不得越过尚未完成的会话切换。
- 交互边界：立即收纳卡片只能表述“已提交”，不能用“已选择成功”暗示后端已经完成；最终成功或失败必须由业务结果明确反馈。
- 并发边界：Codex 绑定读取和写入必须使用稳定的 binding key 串行化，不能使用切换前后可能变化的 conversation ID。
- 来源：用户提供 21:55:06 点击卡片、21:55:18 未绑定失败、21:55:20 才写入绑定的完整时序。

## 2026-07-13 指定时间点的远程日志诊断

- 触发条件：用户明确指出客户端、时间点和实际操作序列，要求依据日志解释异常。
- 规则：必须先取得该时间窗口内从卡片回调、会话路由、Agent 选择到普通消息执行的连续日志，再判断根因。
- 反例：用其他时间段的日志或相似历史修复，直接推断当前事件属于同一问题。
- 正确做法：明确区分已读取日志、代码证据与待验证推论；日志不可访问时应直接说明缺少哪段证据，并请求对应时间窗口原文。
- 来源：用户纠正“安卓飞书窗口约 21:54 切换 Codex 会话后仍提示未绑定”的诊断范围。

## 2026-07-13 DOCX 新编号组必须使用独立抽象编号

- 触发条件：在现有 DOCX 中插入需要从 1 重新开始的编号段落，并通过 OOXML 创建新的编号实例。
- 规则：不能只复制 `w:num` 和设置 `w:startOverride`；还要为新编号组复制独立的 `w:abstractNum`，并在不同渲染器中核对实际编号。
- 反例：新“业绩”段落使用新的 `numId`，但与上一组“内容”共用同一个 `abstractNumId`；WPS 显示 1–3，QuickLook 却继续显示 6–8。
- 正确做法：复制抽象编号、分配新的 `abstractNumId` 和编号标识，再让新 `w:num` 指向它，同时验证 WPS PDF 与 QuickLook 正文编号一致。
- 来源：2026-07-13 简历新增 EMM 业绩后，Word/PDF 等长但逐字符校验发现三处编号差异。

## 2026-07-13 简历技术经历必须确认发行版与 CPU 架构

- 触发条件：用户描述 rootfs、系统裁剪、镜像制作、自动化构建或升级包经历，但未同时明确 Linux 发行版、目标 CPU 和硬件平台。
- 规则：rootfs 与系统镜像能力不绑定 Ubuntu、CentOS 或 ARM；只有用户明确确认后，才能写入具体发行版、CPU 架构及 U-Boot、DTS/DTB 等板级能力。
- 反例：因用户提到 rootfs 和系统镜像，直接把经历表述为 Ubuntu 或 ARM 系统定制。
- 正确做法：先分别确认全部发行版、目标载体和 CPU 架构；涉及多个发行版时先用“Linux 系统定制与交付”概括，再准确列出 CentOS、Ubuntu 等实际范围。
- 来源：2026-07-13 用户先纠正其系统定制经历并非 ARM，随后明确实际范围同时包含多个版本的 CentOS 和 Ubuntu。

## 2026-07-13 Agent 会话切换必须同步窗口 Agent

- 触发条件：用户在飞书或微信通过 `/cc switch`、`/cc new`、`/cx switch` 或 `/cx new` 显式选择某个 Agent 的会话。
- 规则：会话切换成功后必须同时更新 route 级 `agentSessionStore`；只更新 Claude/Codex 自己的 session store，会让下一条普通消息继续按账号默认 Agent 路由。
- 失败边界：目标会话恢复或创建失败时不得覆盖窗口原有 Agent 绑定；窗口 Agent 只能在目标会话操作成功后持久化。
- 测试要求：回归测试必须让平台账号默认 Agent 与目标 Agent 不同，并验证下一条普通消息实际进入目标 Agent。
- 来源：2026-07-13 用户反馈飞书切换到 Claude 会话后，普通消息仍发送给 Codex。

## 2026-07-12 飞书即时任务卡片终态

- 触发条件：飞书需要在 Agent 首个进度事件前给出可见反馈，并把最终回答收敛到同一张卡片。
- 规则：即时反馈与终态通知必须分离；任务登记后立即建卡，成功写入完成终态后不再补发“请查看上方卡片”，失败或停止只有在卡片成功写入对应终态后才补发简短通知。
- 失败边界：开卡失败只提示一次并继续任务；任何终态更新失败都不得发送短通知，必须回退完整普通文本。
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

- 会话列表必须带稳定编号，`/cx switch <编号>` 要使用与 `/cx ls` 相同的排序，避免用户复制长 threadId。
- 修改命令命名时必须同步更新 `isCodexSessionCommand`、帮助文本、命令处理分支和回归测试。
- 旧命令如果不再作为用户入口，就不要继续出现在 `/help`，防止微信侧形成两套说法。

## 2026-04-28 Codex 额度错误

- `usageLimitExceeded` 只是额度耗尽，不代表登录态或工作区失效，不能自动 Stop Codex 进程或清理 thread。
- 用户需要手动切换 Codex 账号时，WeClaw 必须保留当前进程和 thread 映射，避免切账号后 `/cx switch` 遇到已关闭 stdin。
- 只有 `deactivated_workspace` 这类真实工作区/登录态异常才允许触发 runtime invalidation。

## 2026-05-28 Codex Companion attach/detach（历史，已废弃）

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
- 反例：沙箱或受限权限下 `processExists` 把 `operation not permitted` 当成不存在，导致 `weclaw status` 显示“未运行（存在过期 pid 文件）”，但实际 pid 仍是 `/path/to/weclaw start -f`。
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

## 2026-07-12 Codex Desktop no-client 的非幂等 Handoff 恢复

- 触发条件：旧架构中用户重新选择会话且目标控制意图相对持久化状态发生变化，事务实际进入显式 Handoff，并确认没有 Desktop 客户端持有目标 thread。
- 规则：`no-client-found` 只在这类非幂等显式 Handoff 中作为实际 runtime 的 release 证据；系统可以把目标 thread 恢复到 WeClaw app-server，再提交新的 remote 控制意图。
- 反例：把普通消息的 no-client 错误自动改成 app-server 重试；或者把断线、超时、交付状态未知当成自动恢复条件，造成越权接管或消息重复执行。
- 正确做法：只有控制意图变化并实际进入 Handoff 时才依赖 no-client 恢复 runtime；当前 route 已是同一 remote intent 的幂等重新选择只读取本地 runtime binding，不探测 Desktop。普通消息不自动恢复或重试，`desktop` intent 的普通消息也必须拒绝。
- 来源：2026-07-12 Android 飞书机器人发送普通消息后，日志立即返回 `没有 Codex Desktop 客户端可处理请求: no-client-found`。

## 2026-07-12 Agent 会话创建必须由用户显式授权

- 触发条件：普通消息遇到额度耗尽、网络错误、空响应、会话恢复失败，或当前聊天窗口没有有效 Agent 会话绑定。
- 规则：任何错误都不得隐式调用 `thread/start` 或 `session/new`；没有绑定时也不得因普通消息自动创建。只有用户发送 `/new` 或明确点击新建入口，系统才可以创建会话。
- 反例：额度错误后清除原 thread，下一条消息自动创建新 thread；或 stale thread/session 恢复失败后自动重建并重试原消息。这会丢失上下文，并剥夺用户切换其他会话或主动新建的选择权。
- 正确做法：保留原绑定，恢复失败时显式提示用户选择已有会话或发送 `/new`；把“获取或恢复已有会话”和“显式创建新会话”拆成独立 API。
- 来源：2026-07-12 用户明确要求“必须手动 `/new`，直接新建会剥夺用户选择的权利”，并确认未绑定窗口也不能自动创建首次会话。

## 2026-07-12 Codex 会话状态必须跨存储原子同步

- 触发条件：创建、清理或切换 Codex thread，同时存在 ACP thread map、owner registry 和 Messaging route session store。
- 规则：显式创建成功后必须把三个状态源同步到同一 thread；清理时必须同时解除 conversation owner 路由。任何“新会话草稿”都不能依赖下一条普通消息隐式完成。
- 反例：`thread/start` 已返回新 thread，但 owner registry 仍指向旧 `desktop_disconnected` thread，导致后续消息绕过新 thread 并立即报断线；`/cx new` 只写 pending draft，在禁止隐式创建后永远无法完成。
- 正确做法：提供 owner registry 原子 claim-and-bind 操作；`/new`、`/cx new` 立即创建并记录；空工作空间和恢复失败只提示显式选择，不伪装成已创建。
- 来源：2026-07-12 用户反馈“飞书里切换会话、新建会话失败”，生产日志和状态文件显示 ACP thread 与 owner binding 指向不同 thread。

## 2026-07-13 所有启动入口必须复用配置预检

- 触发条件：`restart`、更新后重启或其他入口需要启动后台 WeClaw。
- 规则：所有启动入口必须先执行与 `start` 相同的配置加载、Agent 后端校验和账号前置流程；禁止直接调用 `runDaemon` 绕过预检。
- 反例：`restart` 直接派生后台子进程，旧 Claude CLI 配置只在子进程日志中失败；父进程把未回收的退出子进程当作存活，最终误报“未在超时内完成启动”。
- 正确做法：统一通过 `startConfiguredDaemon` 加载并校验配置，预检失败时直接向用户返回根因且不创建子进程。
- 来源：2026-07-13 用户反馈升级 v0.1.167 后 `weclaw restart` 误报后台子进程启动超时。

## 2026-07-13 外部运行依赖必须在停服前预检

- 触发条件：安装或更新后的 WeClaw 配置了 Claude ACP，但全局 `claude-agent-acp` 缺失、路径失效或能力握手失败。
- 规则：首次安装可以在无隐式提权的前提下显式补齐依赖；自更新不得静默修改 npm 全局环境。任何带重启的入口都必须先验证依赖和配置，再停止当前服务。
- 反例：更新完成后先停止旧服务，再由新进程发现适配器缺失；用户会失去仍可工作的远程入口，只看到启动超时。
- 正确做法：安装脚本提供可跳过、可固定版本的显式安装步骤；普通更新只告警；更新后重启和手动重启共享一次性预检结果，失败时保持旧进程运行。
- 来源：2026-07-13 用户要求优化 WeClaw 安装与更新时的 `claude-agent-acp` 处理。

## 2026-07-14 Codex 本地与远程控制权必须显式移交

- 触发条件：同一个 Codex thread 可能由 Codex Desktop 和 WeClaw 独立 app-server 继续执行。
- 规则：用户通过明确命令选择 Desktop 或远程控制；自动探测只用于验证该选择能否安全执行，不能代替用户决定控制方。
- 反例：根据 Desktop 进程、socket 或旧 owner 缓存自动选择 writer，导致两个进程各自持有不同内存上下文并向同一 rollout 写入。
- 正确做法：分离持久化控制意图与进程内实际运行时；普通消息不得隐式接管或同步探测，直接按 owner tuple、revision、已绑定 runtime 和写入租约执行；发现本地双写时进入显式冲突态。
- 来源：2026-07-14 用户提出在飞书增加明确的所有权移交指令，并确认由使用者主动选择 Desktop 或远程窗口。

## 2026-07-15 WeClaw 测试必须共用持久化 Go 缓存

- 触发条件：在本机运行 WeClaw 的 Go 测试、race 测试或其他会生成 Go 构建缓存的验证命令。
- 规则：统一复用 `GOCACHE=/Volumes/Data/AppData/BuildCaches/weclaw`，不得按任务、测试套件或并行进程创建多个独立缓存目录。
- 反例：为核心测试、全仓测试、race 和 vet 分别创建 `/tmp/weclaw-*` 缓存；每个目录占用数百 MiB，最终并行耗尽磁盘并产生与代码无关的 `no space left on device` 失败。
- 正确做法：普通全仓测试使用 `GOCACHE=/Volumes/Data/AppData/BuildCaches/weclaw go test ./...`；其他 WeClaw Go 验证也复用同一个 `GOCACHE`，需要隔离时优先串行执行，不通过新增缓存目录隔离。正式发布入口必须统一执行缓存初始化：`WECLAW_GOCACHE` 优先于调用方 `GOCACHE`，本机 Darwin 自动选项目共享缓存，其他主机显式回退到 `go env GOCACHE`。
- 来源：2026-07-15 最终发布前复验因多套临时 Go 缓存耗尽磁盘；用户明确要求以后所有 WeClaw 测试共用固定缓存。

## 2026-07-15 Claude session 的目录事实与写入所有权必须分离

- 触发条件：多个微信或飞书 route、WeClaw ACP runtime 和本地 Claude CLI 可能继续使用同一个 Claude session。
- 规则：`session/list` 只回答有哪些真实 session；远程写入必须由持久化 control intent 的 owner tuple 和 revision 决定。选择或新建先提交 `remote`，再恢复 ACP runtime；恢复失败保留 owner 和选择并持久化 `resume_failed` 写入门禁。本地交接先提交 `local`；普通消息不得根据 ACP runtime 或最近 binding 隐式接管。
- 反例：把 conversation runtime、session binding 或 `session/list` 中存在目标 session 当作写入授权；两个窗口会各自恢复同一 session 并并发写入，补偿失败还可能覆盖新赢家。
- 正确做法：binding 锁外层配合排序 session 锁，copy-on-write 一次持久化 binding/control；任务登记前和 prompt 前复核 session/revision；只有窗口 Agent 持久化失败才在 after-image 仍匹配时回滚选择，runtime 失败保持 `remote + resume_failed`。v2 多 binding 冲突不选赢家。
- 来源：2026-07-15 Claude 远程会话“选择即接管”治理。

## 2026-07-17 Codex remote owner 是飞书写入的唯一授权门禁

- 触发条件：当前飞书 route 已持久化持有同一 Codex thread 的 `remote` owner，但 Desktop 探测、checkpoint、app-server 恢复或旧 conflict 快照异常。
- 规则：owner tuple 决定“谁能写”，runtime 只决定“怎么写”；除非飞书显式释放为 `desktop`，技术探测和恢复失败不得撤销 owner、制造持久写入冲突或在普通消息入口拒绝飞书。该规则覆盖 2026-07-14 中“普通消息不得恢复 runtime”的旧约束。
- 反例：`CurrentCodexRuntime=unknown/conflict` 直接拒绝普通消息，或把 `thread/resume: no rollout found`、Desktop IPC 不可达、checkpoint 变化统一写成 `RuntimeConflict`，导致飞书虽仍持有 owner 却无法写入。
- 正确做法：同 route 的 remote owner 消息直接进入 writer 流程，unknown/conflict 在 `RunCodexTurn` 内自动恢复；只有真实 writer lease 或 observed turn 身份冲突保留并发保护。若 `/cx new` 已明确创建但首条 turn 前 thread 未 materialize，可原子补建并迁移 owner；已有历史的 thread 不得自动替换。
- 来源：2026-07-17 用户再次明确“飞书是第一写入优先级，除非飞书释放控制权，不然不能阻止飞书写入”，并用 `no rollout found` 实机日志确认旧 runtime 门禁违背该规则。

## 2026-07-17 Codex 切换必须同步 ACP conversation 指针

- 触发条件：飞书窗口切换到一个已由 WeClaw app-server 持有的 Codex thread，而同一 conversation 在 ACP 持久化 state 中仍指向旧 thread。
- 规则：显式切换或接管成功后，只要目标 runtime 确认为 `WeClaw app-server`，必须同步 ACP `conversationID -> threadID` 映射并清除旧 `resumeOnFirstUse` 标记；owner registry 更新不能替代 ACP thread map。
- 反例：`/cx switch` 已把 owner 和 workspace 选到新 thread，但 `HandoffCodexRuntime` 因目标已是 WeClaw runtime 直接返回，没有更新 `a.threads`；下一条普通消息从旧 ACP 映射恢复已归档 thread，报 `session is archived`。
- 正确做法：复用已知 WeClaw runtime 时仍调用同一绑定 helper；只有 runtime 激活成功后才持久化 ACP 映射。回归测试必须同时断言内存 `threads`、`resumeOnFirstUse` 和 state 文件。
- 来源：2026-07-17 用户反馈“切换到 codex 会话后，发送信息一直报错”，实机日志显示切换到新 thread 后普通消息仍 `resume restored thread` 到旧归档 thread。

## 2026-07-20 Codex 切走不刷屏，切回重建当前任务卡

- 触发条件：飞书或微信切换到 Codex App 正在执行的 thread 后，又需要浏览 `/cx ls` 或切换到其他 Codex 会话。
- 规则：飞书窗口可以按用户选择自由浏览和切换 Codex 会话；只读导航命令不得被 active task 拦截。切到其他会话后旧任务不应在当前消息流持续刷屏，但任务执行身份和结构化快照必须保留；真实切回仍在运行的任务时，应在消息底部重建一张当前卡。
- 反例：把 `/cx ls`、`/cx cd` 或会话选择按钮一律挡在“当前任务正在执行”之后，导致用户切到运行中会话后无法再切走。
- 正确做法：用 frontend binding 的前后快照判断真实 A→B→A；切回 A 时从 `activeTasks` reducer 读取最新进展，新建底部卡后原子替换 progress stream，再把旧卡标记为“已转移”并停止 streaming。终态准备与重锚使用同一会话锁，只有最新卡可以进入 terminal outbox；A→A 重复选择不得重建。其他窗口、其他 route 或其他任务的展示不能被当前窗口清理。
- 来源：2026-07-18 用户明确窗口可自由切换；2026-07-20 用户反馈切回 A 后原卡虽恢复更新但已被消息刷到上方，要求优化当前任务可见性。

## 2026-07-18 耗时卡片操作必须回写原卡终态

- 触发条件：飞书会话卡片按钮执行 `/cx switch`、`/cc switch` 等可能超过同步回调预算的操作。
- 规则：快速完成时通过卡片回调直接替换原卡；超过回调预算后使用原消息 ID 异步 patch 同一张卡。成功、失败和超时都必须形成可见终态，只有 patch 失败时才显式降级为单独消息。
- 反例：按钮点击后原卡永久停在“已受理”，最终结果固定另发；或飞书重投旧事件时再次返回“处理中”卡片，覆盖已经写入的终态。
- 正确做法：保留原消息关联，异步结果通过 `im.message.patch` 回写；同一窗口继续按分发顺序执行；重复事件只返回 toast、不携带卡片；回写错误记录日志并发送完整文本兜底。
- 来源：2026-07-18 用户反馈“切换会话怎么没有在卡片里更新结果”。

## 2026-07-18 Codex 模型配置必须绑定 thread

- 触发条件：飞书或微信窗口已绑定 Codex thread，用户通过 `/model` 或 `/reasoning` 修改当前会话配置。
- 规则：当前会话必须通过 app-server `thread/settings/update` 更新，且只影响该 thread 后续 turn；新会话默认值与当前 thread 配置是不同状态，禁止用进程级默认值覆盖所有 route。
- 反例：命令只修改 `ACPAgent.model/effort`，再在每次 `turn/start` 或 `thread/resume` 注入共享值；表面上下一轮生效，实际会串到其他会话，卡片还会把新 thread 默认值误报为当前 thread 状态。
- 正确做法：默认值只用于 `thread/start`；当前配置从 `thread/start`、`thread/resume` 响应和 `thread/settings/updated` 通知缓存，并按 wire sequence 拒绝旧通知。`thread/read` 不返回模型配置，缓存缺失时只能使用 rollout 作为展示回退，不能把全局默认值当权威当前值。
- 来源：2026-07-18 用户反馈“切换 Codex 的模型及推理强度不能对当前会话生效”，协议核对确认 `thread/settings/update` 才是当前 thread 的正式配置入口。

## 2026-07-18 内置命令必须区分精确语法、route 状态与配置作用域

- 触发条件：同一个短入口既可以发送 Agent 消息又可以执行会话命令，或状态命令同时涉及当前绑定与新会话默认值。
- 规则：内置命令只消费精确 token 和合法参数数量；无参数状态必须按当前窗口 route 读取真实 workspace；当前会话配置与新会话默认值必须在文案中明确区分。
- 反例：`/cc status 请解释` 被吞成状态命令，`/cwdfoo` 被当成 `/cwd foo`，无参数 `/cwd` 返回占位值，或把 `/cx model status` 的新 thread 默认值标成当前 thread 配置。
- 正确做法：保留 `/cc <内容>` 时为保留词命令建立严格 grammar；`/cwd` 使用独立 token 检测并按 route binding 只读查询；`/model`、`/reasoning` 显示当前/默认作用域，Agent 专用 `model status` 明确标为新会话默认配置。
- 补充：route 已显式选择的 Agent 即使被移除或暂时不可用，也不能静默改用平台默认 Agent；状态和写入入口必须保留原选择并明确失败，避免跨 Agent 误操作。

## 2026-07-19 认证切换的未知终态必须跨重启失败关闭

- 触发条件：共享 Codex Host 在认证切换的 stop、auth 投影、start、验证或回滚阶段崩溃，或者 profile 索引与 Host 身份元数据只有一侧提交成功。
- 规则：触碰 Host 前必须持久化 `switching` journal；`switching` 和 `rollback_failed` 在新进程中仍是不可写状态。跨索引与 Host 元数据的在线保存必须先在同一账户锁内准备，再以补偿恢复旧元数据；补偿失败也要持久化失败终态。
- 反例：只把 gate failed 放在进程内存，重启后构造新 gate 自动变成 running；或先 `store.Save` 再写 Host 元数据，第二步失败却保留新 active profile。
- 正确做法：把非终态 journal 作为持久化安全门禁；只有成功、完整回滚或停服后的显式离线 `use` 才恢复可写。替换/删除旧 secret 的失败也必须保留待清理引用并重试，不得忽略 `Delete` 错误。

## 2026-07-19 进展快照与终态投递必须使用不同可靠性边界

- 触发条件：任务卡实时进展正常，但进程在终态 CardKit 更新或最终文本发送期间断线、重启，或旧 watcher 在终态后继续回调。
- 规则：进展是可覆盖的最新快照，必须保留来源 sequence 并在终态建立水位线；终态是不可丢的交付记录，必须先持久化 outbox 再执行平台网络写入。
- 幂等边界：CardKit 重试复用同一 UUID 与 sequence，飞书文本复用消息 UUID，微信分片复用 client_id；每个阶段成功后单独原子提交，后续失败不得重放已提交阶段。
- 能力边界：只有底层 adapter 真实实现稳定去重键时才允许暴露幂等接口；缺失时必须失败关闭，不能静默降级到普通发送后再由 outbox 重试。
- 交付语义：outbox 提供 at-least-once 和跨重启恢复，不虚构全平台 exactly-once；附件与远程图片未纳入 v1 时必须明确保留原有 best-effort 边界。

## 2026-07-19 stdio ACP 子进程只能有一个 Wait 所有者

- 触发条件：ClaudeHost 或其他标准 ACP 子进程自然 EOF、初始化失败、显式 Stop 或进程组强制清理并发发生。
- 规则：stdout reader 负责唯一一次 `Cmd.Wait` 并发布完成 channel；其他路径只发出关闭/终止信号并等待同一 channel。旧 reader 必须按 wire epoch 隔离，不能清理新 generation。
- 反例：readLoop 在 EOF 时先清空 `a.cmd` 而不 Wait，之后 Stop 已拿不到进程句柄，留下 zombie；或者 readLoop 与 Stop 各自 Wait 同一个 `exec.Cmd`。
- 正确做法：自然退出先等待短暂收敛，异常只关闭 stdout 时再优雅终止并最终清理进程组；所有等待者共享同一完成结果。

## 2026-07-19 外部账号标识与扫码登录都必须在边界收敛

- 触发条件：微信返回的 `ILinkBotID` 参与凭据/context-token 文件名，或 Web 用户重复点击扫码登录。
- 规则：所有账号文件路径统一使用同一个受限字符编码加哈希的安全 ID，禁止把外部原文交给 `filepath.Join`；兼容映射造成的文件名碰撞必须在保存和加载时失败关闭。同一 Web 服务只允许一个 active 登录 poll，新会话取消旧 context 并把旧状态固定为 expired。
- 反例：只替换 `@`、`.`、`:`，却保留 `/`、`\\` 或绝对路径；每次点击都启动独立五分钟后台 poll，晚到旧回调仍可保存旧凭据。
- 正确做法：在 `ilink` 边界规范化并复用到所有持久化路径；active session、cancel 和 terminal status 在同一锁下更新，保存凭据前再次确认会话仍是当前 active。

## 2026-07-19 正式发布只能有一个权威执行入口

- 触发条件：本地发布脚本和 GitHub Actions workflow 分别实现测试、构建、上传与校验。
- 规则：`scripts/release.sh` 是唯一稳定版发布实现；workflow 只配置权限、checkout clean `main`、Go 版本和 `GH_TOKEN`，然后调用该脚本。
- 反例：workflow 自己 checkout 既有 tag、运行较少门禁并直接用 Release action 上传，导致本地与远端发布语义漂移。
- 正确做法：测试 workflow 的“委托权威脚本”契约，而不是要求 workflow 复制脚本里的矩阵、staticcheck、govulncheck 和资产校验文本。
