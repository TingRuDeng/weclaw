# WeClaw 全面审查与修复优化方案（2026-09-07）

## 状态

基于 `main` 提交 `1033f7a` 的只读审查结论与分阶段修复计划。本文是执行计划，不是当前 authority doc；产品事实以 `docs/AI_CONTEXT.md` 和源码为准。每一项完成后应在本文对应条目标注提交号，全部完成或方案变更时归档到 Git 历史。

## 审查范围与方法

- 覆盖包：`agent/`（ACP 运行时与 Codex Host 生命周期）、`messaging/`（命令、任务、进度、审批、terminal outbox）、`feishu/`、`wechat/` + `ilink/`、`api/`、`observability/`、`codexauth/`、`internal/securefile`、`internal/accountstore`、`internal/remotefetch`、`config/`、`cmd/`、`web/`、`scripts/`、全部文档。
- 方法：源码阅读 + 关键结论逐条复现（如微信 Markdown 转换、`SanitizeText` 泄漏、keychain 长度上限、飞书 300317 吞错均以实际执行或读取上游依赖源码验证）。
- 工具链基线（干净 worktree）：`go build`、`go vet`、`staticcheck`（`all`）、`go mod tidy -diff`、`govulncheck` 全部零告警；`go test ./...` 17 包全过；`go test -race ./feishu ./messaging` 干净。
- 前两轮飞书交互审查中已修复的 14 项（未知命令拦截、echo 兜底、过期消息提示、未绑定会话短路、群聊审批等待提示、审批超时回写、结果卡 fence 切分、首次接入 checklist 等）本文不再重复，只保留仍未处理的遗留项。

## 总体判断

工程质量高：静态分析零告警、约 2800 个测试全绿、安全防护（SSRF、路径穿越、密钥落盘、CSRF、常量时间比较、进程身份复核）远超同类工具。问题集中在五处：

1. **可靠性边界**：几处"网络调用持锁"与"无超时 HTTP 客户端"叠加可让任务永久卡死；Host 断线时输入投递状态被误报为确定失败，会诱导用户重发造成重复执行。
2. **信任模型过平**：任一白名单用户即主机管理员。
3. **敏感信息脱敏不足**：trace 里的 `SanitizeText` 对真实凭据形态大多不生效。
4. **微信平台体验明显落后**：同步型 Agent 会阻塞全部微信用户；Markdown 转换破坏路径/代码/链接。
5. **运维可观测性与文档**：CLI 错误双打、`status` 信息量不足、README 过时安全规则、AI_CONTEXT 不可维护。

## 优先级定义

- **P0**：会导致数据错误、重复执行、任务永久卡死、凭据泄漏或所有用户失联；一周内处理。
- **P1**：明显影响正确性、安全边界或核心体验；两周内处理。
- **P2**：体验与一致性改进；按迭代排入。
- **P3**：工程健康、重构、文档补全；长期。

每项给出：问题、证据位置、修复方案、验证方式。工作量：S（≤半天）、M（1–2 天）、L（>2 天）。

---

## P0

### P0-1 Host 断线时 turn/start、turn/steer 的响应丢失被报为确定失败

- **问题**：`turn/start` 写出后 Host 断线，pending RPC 以 `agent error: ACP runtime exited` 失败，不携带 `ErrCodexInputDeliveryUnknown` 哨兵；下游把 attempt 记为 `Rejected`、释放 writer lease，用户看到原始错误并重发，上游可能已经开始执行 → 重复执行。
- **证据**：`agent/acp_read_loop.go:295-306`（`failRuntimeWaitersUncertainWithCause` 仅在 `cause != nil` 时传哨兵，多数路径 `cause == nil`）；`agent/acp_rpc.go:147-157`；`agent/codex_app_server_turn.go:163-203, 273-278`；`agent/codex_runtime_turn.go:142-171`。
- **修复**：`failRuntimeWaitersUncertainWithCause` 在 `cause == nil` 时补默认哨兵 `errACPRuntimeLost`，使 `callWithSequence` 对输入类方法统一包裹 `ErrCodexInputDeliveryUnknown`；显式 `Stop` 路径（`failRuntimeWaiters`）保持不变。
- **验证**：新增测试：`turn/start` 写出、响应尚未到达、readLoop EOF → 断言 `errors.Is(err, ErrCodexInputDeliveryUnknown)`，attempt 状态为 unconfirmed，用户提示为"请先在 Codex App 核对，勿重发"。
- **工作量**：S。

### P0-2 飞书流式卡 `flushPresentation` 使用 `context.Background()` 且 SDK 客户端无 HTTP 超时

- **问题**：`lark.NewClient` 未设 `WithReqTimeout`，SDK 回退到 `http.DefaultClient`（无超时）。一次挂起的 CardKit 请求会永久持有卡片操作锁与 `ioMu`；`PrepareTerminalWithState` 在持 `progressSession.streamMu` 时阻塞，任务永不完成、route 永久占用，`/stop` 无效。
- **证据**：`feishu/stream.go:136-142`；`feishu/adapter.go:63-66`；上游 `oapi-sdk-go/v3/core/httptransport.go:29-32`。
- **修复**：(a) `lark.NewClient(..., lark.WithReqTimeout(30*time.Second))`；(b) `flushPresentation` 使用与 `flushPendingUpdate` 一致的、派生自 stream 生命周期并带 10 s 超时的 ctx。
- **验证**：用挂起的 fake CardKit 服务端测试 `flushPresentation` 在超时后返回、`PrepareTerminalWithState` 不阻塞。
- **工作量**：S。

### P0-3 恢复终态时旧 sequence 触发的 300317 被当成成功，卡片永久"思考中"

- **问题**：`ignoreCardKitUpdateError` 把 300317（sequence 比较失败）视为成功；持久化 reference 的 `Sequence` 只在部分路径刷新（节流 flush、重开流式重试、`setApprovalWaiting`/`refreshApprovalWaiting` 都不刷新）。重启后 recovery 以 `Sequence+1/+2` 提交终态，若落后 ≥2 则两次操作都得 300317 → `markDelivered` → outbox 显示成功，卡片实际仍在流式"思考中"，结果卡照常发送。
- **证据**：`feishu/stream.go:1166-1177, 1146-1149, 221-233, 508-521`；`feishu/approval_waiting.go:24, 47`；`messaging/progress.go`（`persistActiveStreamRecoveryLocked` 触发点）。
- **修复**：(a) 所有到达网络的 sequence 递增之后都调用 durable-reference 变更通知；(b) 终态操作（`deliverFeishuTerminalCheckpoint`）不吞 300317：改为可重试错误，重试前重新读取/推进 sequence；至少要让该条目可见地进入 dead-letter。
- **验证**：测试：模拟 reference sequence 落后 2 → 终态投递必须重试并最终成功或 dead-letter，不能标记 delivered。
- **工作量**：M。

### P0-4 `observability.SanitizeText` 对真实凭据形态大多不脱敏

- **问题**：正则两侧 `\b` 且 `_` 属于单词字符，`app_secret=`、`tenant_access_token:`、`OPENAI_API_KEY=`、`client_secret` 全部漏过；无裸 token 模式（`sk-`、`sk-ant-`、`ghp_`、`xox[abprs]-`、`AKIA`、飞书 `t-g…`/`u-…`、PEM）；只识别 ASCII `:`/`=`，中文全角冒号漏过。trace `Summary` 直接接收 `err.Error()` 和 Codex 计划文本，用户在聊天中粘贴的密钥会原样进入 `trace.jsonl`。实测 6 个样本 4 个泄漏。
- **证据**：`observability/trace.go:210`；`messaging/agent_task_lifecycle.go:118`；`agent/codex_plan_events.go:42`。
- **修复**：key 改为后缀匹配 `(?:^|[^A-Za-z0-9])[A-Za-z0-9_-]*(?:token|secret|key|password|passwd|authorization|cookie)`；分隔符接受 `[:=：＝]`；新增裸 token 正则（`sk-[A-Za-z0-9_-]{16,}`、`ghp_`、`xox[abprs]-`、`AKIA[0-9A-Z]{16}`、`[tu]-[A-Za-z0-9]{20,}`、`-----BEGIN … PRIVATE KEY-----` 块、`://user:pass@`）。同时把 `redactProtocolValue`（`observability/protocol.go:268-286`）的 key 匹配改为同一后缀规则。
- **验证**：表驱动泄漏测试覆盖 ≥40 个真实形态样本，全部必须 `[REDACTED]`。
- **工作量**：S。

### P0-5 macOS Keychain 后端无法保存真实 `auth.json`，错误被误报为"凭据库不可用"

- **问题**：go-keyring darwin 后端把整个 secret 拼进 `security` 命令行，`len(command) > 4096` 直接 `ErrSetDataTooBig`。本机 `~/.codex/auth.json` 为 4072 字节，加上 service/account 与转义必然超限。`codexauth` 把该错误映射为 `CodeFileStoreConsentRequired`（"系统凭据库不可用"），把用户推向 0600 文件后端而不说明真实原因。现有测试只用约 300 B 的 fixture。
- **证据**：`codexauth/keyring.go:34`；上游 `go-keyring keyring_darwin.go:87-88`；`codexauth/store.go:800-806`。
- **修复**：短期：识别 `keyring.ErrSetDataTooBig`，返回独立错误码与明确中文提示（"凭据体积超过系统凭据库命令行上限"），并在 `doctor` 展示；中期：更换为基于 Security.framework `SecItemAdd`/`SecItemUpdate` 的实现或把 secret 拆分为多条目存储。
- **验证**：≥4 KB payload 的 keychain 测试（可用 fake 后端验证错误映射；真实 keychain 在 macOS CI 上验证）。
- **工作量**：S（短期）/ M（中期）。

### P0-6 同步型 Agent 阻塞全部微信用户收消息

- **问题**：iLink `processUpdateResponse` 等本批全部消息 ack 后才发下一次 `GetUpdates`；ack 在 handler 返回后 close。Codex/Claude 走后台 goroutine，但 gemini、cursor、kimi、opencode、pi、copilot 等所有其他 Agent 类型在 `runSynchronousAgentMessage` 中同步执行。一个用户的任务跑 5 分钟，期间所有微信用户（含其本人的 `/stop`、审批回复）都收不到；`TaskTimeoutSeconds` 默认 0。watchdog 5 分钟"空闲"后还会重启 monitor 导致同批重投。
- **证据**：`ilink/monitor_queue.go:16-38, 104-136`；`messaging/agent_execution.go:95-98, 122`；`wechat/adapter.go:153-178`。
- **修复**：`runSynchronousAgentMessage` 在 admission 之后改为 goroutine 执行（与 Codex/Claude 路径一致），ack 在 admission 完成后即可返回；保留互斥锁与 pending 语义。
- **验证**：测试：两个用户同时发消息，第一个 Agent 阻塞 3 s，第二个用户的 `/status` 必须在 3 s 内得到回复。
- **工作量**：M。

### P0-7 微信 Markdown 转纯文本破坏路径、代码与链接

- **问题**：实测 `/src/__init__.py → /src/init.py`、`2**3**4 → 234`、`[文档](https://…) → 文档`（URL 被删）、代码块内容被二次当 Markdown 处理（`# load config → load config`，`- x → • x`）。对编程助手而言复制的路径/命令是错的。
- **证据**：`wechat/markdown.go:9-30`。
- **修复**：先用占位符保护围栏代码与行内代码，处理完其他规则后原样还原；链接渲染为 `文字 (url)`；图片渲染为 `[图片] url`；粗体 `__x__` 要求两侧词边界或直接不支持；代码块内不插空行、不改缩进；表格转为 `列1 | 列2` 行。
- **验证**：表驱动测试覆盖路径、双下划线、乘法、链接、图片、围栏代码、嵌套列表、表格。
- **工作量**：S。

### P0-8 微信 `/progress stream` 每条 delta 发一条消息

- **问题**：微信 `Capabilities` 无 `Streaming`，但 `ensureStreamLocked` 只看模式；微信 `OpenStream` 返回 `textStream`，其 `Update` = Typing + `SendText`；stream 模式下 `shouldSendProgress` 跳过 `MaxProgressMessages`。用户通过 `/progress stream` 即可触发刷屏。`normalizePlatformProgressConfig` 只对飞书归一。
- **证据**：`messaging/progress.go:980-984`；`messaging/progress_utils.go:34`；`wechat/replier.go:146-148, 196-206`；`messaging/progress_config.go:73-82`。
- **修复**：`normalizePlatformProgressConfig` 增加参数或在 `progressSession.start` 中判断 `!reply.Capabilities().Streaming` 时把 `stream` 降为 `summary`；`/progress` 帮助在非流式平台隐藏 `stream`。
- **验证**：测试：微信 replier + `Mode: stream` → 5 分钟内发出的进度消息数 ≤ `MaxProgressMessages`。
- **工作量**：S。

### P0-9 CLI 错误双打并夹英文 cobra 模板

- **问题**：未设 `SilenceUsage`/`SilenceErrors`，无中文 `UsageTemplate`。任何 `RunE` 错误输出为 `Error: <中文>` + 英文 `Usage:/Flags:` 块 + 同一中文错误再一遍。
- **证据**：`cmd/root.go:44-58, 80-86`。
- **修复**：`rootCmd.SilenceUsage = true; rootCmd.SilenceErrors = true`；`Execute()` 统一打印一次错误；新增与 `HelpTemplate` 一致的中文 `UsageTemplate`，参数校验类错误显式打印一次用法。
- **验证**：`cmd/help_text_test.go` 增加用例：错误输出恰好一次、不含 `Usage:`/`Flags:` 英文字面量。
- **工作量**：S。

---

## P1

### P1-1 账号切换事务使用调用方可取消 ctx，取消后留下不可恢复的 fail-closed 状态

- **问题**：`switching` journal 落盘后，`stopManagedHost`/`startManagedHost`/验证/回滚都用请求 ctx（HTTP `r.Context()` 或消息 ctx）。Ctrl-C 或客户端断开 → stop 被当超时立即 `SIGKILL -PGID` → gate `failed`、journal 停在 `switching`，或回滚同样失败进入 `rollback_failed`。所有 turn 被拒，需停服离线 `use`。
- **证据**：`agent/codex_account.go:474, 503, 507, 558-587`；`agent/codex_host_supervisor.go:398, 496`。
- **修复**：journal 落盘后切换为 `context.WithTimeout(context.WithoutCancel(ctx), budget)` 执行 stop/写入/start/验证/回滚。
- **验证**：测试在 `startManagedHostCall` 内取消 ctx，断言结果 `rolled_back` 而非 `rollback_failed`。
- **工作量**：S。

### P1-2 回滚成功但结果记录写盘失败时 journal 停在 `switching`

- **证据**：`agent/codex_account.go:488-492`；`agent/codex_account_sync.go:553-556`；`docs/AI_CONTEXT.md:86` 描述（"恢复为 failed"）与代码（只 `gate.fail()`，不重写 journal）不一致。
- **修复**：事务外重试一次 journal 写入；失败时输出明确的操作指引；修正 AI_CONTEXT 措辞。
- **工作量**：S。

### P1-3 Host 重连后旧 epoch 的待处理审批/用户输入被重放到新连接

- **问题**：`pendingTurnInteractions` 只在 resolve/settle/forget 时清理，断线/重连路径不清；`registerTurnObserver` 会把它们重放给新 watcher，`approval.Respond` 用旧 request ID 写入新连接。用户被要求决定"幻影"请求。
- **证据**：`agent/codex_interaction_broker.go:283-285`；`agent/codex_app_server_host.go:742-759`；`agent/acp_read_loop.go:250-262`；`agent/turn_channel_registry.go:81-85`；`agent/acp_permission_bridge.go:47-54`。
- **修复**：`codexTurnEvent` 打上 wire epoch；epoch 变化时对 app-server 来源的 pending 交互执行 `settleCodexTurnInteractionsLocked`；`Respond` 在 epoch 不匹配时拒绝并把飞书卡更新为"已失效"。
- **工作量**：M。

### P1-4 `stopManagedCodexHostLocked` 发信号前不重新验证进程身份

- **问题**：身份在函数入口校验，但到 `SIGINT`/`SIGKILL` 之间有完整 `ps` 预检、可选 exec、断线处理等；`waitCodexHostProcessExit` 只用 `kill(pid,0)`。`--force` 冲突路径已正确实现"升级 SIGKILL 前复核"，本路径没有。
- **证据**：`agent/codex_host_supervisor.go:343, 377-391, 458-464`；对照 `agent/codex_host_conflict_stop_unix.go:19-45`。
- **修复**：复用 `stopCodexConflictProcessGroup`/`verifyCodexConflictMemberCurrent`，用 metadata 构造 proof；ctx 取消不得等同超时直接 SIGKILL。
- **验证**：新增真实子进程的 SIGINT/SIGKILL 分支测试（当前 20 个账号/重启测试全部 stub 了 `stopManagedHostCall`）。
- **工作量**：M。

### P1-5 `codexAdmissionMu` 持锁跨无上限 RPC

- **问题**：账号切换全程持 `codexAdmissionMu`（含 `thread/list` 分页、`account/read`、`ps`、keyring I/O、Host 停启）无每次调用超时；`runCodexTurn`/`SteerCodexInput` 用阻塞 `Lock()`。一次挂起的 app-server 调用让所有 thread 的所有 turn 无限等待。
- **证据**：`agent/codex_account.go:325`；`agent/codex_runtime_turn.go:32, 68`。
- **修复**：变更前的只读 RPC 统一 `context.WithTimeout(ctx, 30s)`（参照 `codex_restart.go:839`）；turn admission 改为 ctx-aware 的 try-lock/等待。
- **工作量**：M。

### P1-6 扁平信任模型：所有白名单用户都是主机管理员

- **问题**：`isAdminMessage` 直接返回 `HasAuthorizedAccess()`；`/update`、`/restart`、`/feishu users approve|revoke`、`/cwd`、`/mode yolo` 共用该门禁。任一白名单账号被盗 = 主机失守。
- **证据**：`messaging/admin_commands.go:129-133`；`messaging/feishu_identity_commands.go:39`。
- **修复**：新增按 bot/平台的 `admin_users`（或 `roles`）配置层，Registry 签发的授权能力区分 `chat` 与 `admin`；`/update`、`/restart`、身份管理、`/cwd` 跨出配置根目录、`/mode yolo` 要求 `admin`。旧顶层 `admin_users` 继续告警忽略，避免误扩权。`/help manage` 只对 admin 显示。
- **验证**：授权矩阵测试（chat/admin × 私聊/群聊 × 每条管理命令）。
- **工作量**：L。

### P1-7 `/cwd` 无限制且全局生效

- **证据**：`messaging/cwd_command.go:153-178, 192-206`；`config.LegacyAllowedWorkspaceRoots`（`config/config.go:19`）无读者。
- **修复**：让 `allowed_workspace_roots` 真正生效（为空时回退为当前配置的 Agent `cwd` 与工作空间登记根）；cwd 按（route, agent）绑定而非对全部 Agent `SetCwd`；写审计。
- **工作量**：M。

### P1-8 远程 `/update` 仅同源校验和

- **证据**：`cmd/update_checksum.go:12-28`；`cmd/update_release.go:168`。
- **修复**：对 `checksums.txt` 用 minisign/cosign 签名并内嵌公钥，`update` 校验签名；`/update` 要求 admin + 私聊（依赖 P1-6）。
- **工作量**：M。

### P1-9 网络调用持 `progressSession.streamMu`，并存在 `tasks.mu → task.mu → streamMu` 边

- **问题**：`progress.go` 多处在持 `streamMu` 时做 CardKit/IM I/O；Agent 事件回调也要拿 `streamMu`，因此每次飞书调用都会阻塞 Agent 事件泵。`codex_frontend_detach.go:99-129` 在持 `h.tasks.mu` 与 `task.mu` 时调用 `claimDetachWithoutTerminal()`（取 `streamMu`），其调用方还持 `codexFollowerDeliveryMu` 写锁；一次慢 CardKit 调用会阻塞全局任务 admission 并饿死 outbox worker。与 `d165e66` 修的死锁同形（优先级反转，非环）。
- **证据**：`messaging/progress.go:791, 855-859, 1001, 1230, 1269-1281, 1420-1455`；`messaging/codex_frontend_detach.go:99-129`；`messaging/platform_access_refresh.go:22`；`messaging/codex_thread_release.go:38`。
- **修复**：短期：`claimDetachWithoutTerminal` 改为原子 CAS，或在释放 `tasks.mu`/`task.mu` 后再 claim；中期：`streamMu` 内只做快照，网络发送移到锁外（"snapshot under lock, send outside"）。
- **验证**：`-race` 套件 + 注入 2 s 延迟 fake CardKit 的并发测试，断言其他 route 的 admission 不受影响。
- **工作量**：M（短期 S）。

### P1-10 飞书任务卡 JSON 软上限 2.8 MB 与平台建议值相差近 100 倍

- **问题**：`feishuCardJSONSoftLimitBytes = 2_800_000` 对应接口参数字符数硬上限，飞书对 200860 的说明是"建议 30 KB 以内"，组件上限 200。`stream_timeline_limit` 默认 0、commentary 全文累计不限条，长任务会撞 200860 且续卡逻辑不触发。
- **证据**：`feishu/stream.go:18`；`config/progress.go:27`；`messaging/task_progress_timeline.go:109-127`。
- **修复**：软上限降到 28 KiB（与结果卡 24 KiB 同量级）；`stream_timeline_limit` 默认改为 60；预检超限即走现有续卡路径。
- **验证**：更新 `feishu/stream_test.go` 常量断言；新增长 commentary 触发续卡的测试。
- **工作量**：S。

### P1-11 微信审批：中文/数字回复不被理解，提示词谈"按钮"

- **证据**：`messaging/approval_options.go:30-54`；`messaging/approvals.go:776-791`；`wechat/replier.go:150-156`。
- **修复**：别名加 `同意/允许/是/y/yes/1 → allow`、`拒绝/否/n/no/2 → deny`；非流式平台选项渲染为 `1. 仅本次允许（回复 1 或 /approve CODE）`；`!Capabilities().Streaming` 时去掉"按钮/卡片"措辞；Claude 有 pending 审批且文本不匹配时回复审批提示而不是转发给 Agent。
- **工作量**：S。

### P1-12 README 过时安全规则

- **证据**：`README.md:394-395`、`README_CN.md:396-397`（`codex_host_mode` 只列三种；`codex_multi_frontend` "强制 daemon"），与 `config/config.go:233`、`README:77,79`、`AI_CONTEXT:82` 矛盾。
- **修复**：改写为四模式描述，同步中英文。
- **工作量**：S。

### P1-13 `weclaw status` 信息量不足且日志路径误导

- **证据**：`cmd/status.go:16-40`；`cmd/daemon_log.go:110-113`（只在 `WECLAW_DAEMON_CHILD=1` 时写文件）。
- **修复**：运行中时调用 `/api/runtime` 输出版本、平台与 bot、活动任务数、outbox 积压/dead-letter、Codex Host 模式与 socket、API 地址；systemd 模式日志提示改为 `journalctl -u weclaw`；`模式:` 标签改为 `进程模式:`。
- **工作量**：S。

### P1-14 `/api/send` 返回原始错误且不记日志；`/api/runtime/drain` 可在已准备重启时重开 admission

- **证据**：`api/send.go:65, 69`；`api/server.go:318-322`。
- **修复**：`writeJSONError(..., SanitizeText(err.Error()))` + 服务端日志；drain 取消在 restart prepared 时返回 409 或走 `CancelRuntimeRestart`。
- **工作量**：S。

### P1-15 Go 侧 provider 迁移不支持 `CODEX_SQLITE_HOME`

- **问题**：`codex_provider_runtime.go:58`、`codex_provider_migration.go:105` 硬编码 `filepath.Join(codexHome, "state_5.sqlite")`，而 `codex_app_daemon_environment.go` 与 daemon 复用代码已支持独立 SQLite 目录。共享 Host + 独立 SQLite home 的安装上，Go 路径会报 "thread 不存在"或迁移错误数据库。同样缺口在 `cmd/doctor_dependencies.go:214`、`messaging/codex_app_sessions.go:73,132`。
- **修复**：把 `CodexSQLiteHome` 贯穿到 `codexProviderMigrationRequest`、doctor 与 app sessions 读取。
- **工作量**：M。

### P1-16 `scripts/codex-provider-switch.sh`（提交 `1033f7a`）

- **问题**：(a) `os.replace` 之后才删 `-wal`/`-shm`，若在窗口内崩溃，热 WAL 会把旧行重放到新库（已在脚本外复现）；(b) 脚本把用户库 `journal_mode` 永久改为 DELETE，与 README:397 "不迁移 WAL/SHM" 承诺矛盾；(c) 备份目录、manifest 格式、provider 过滤范围与 Go 实现互不兼容（`backups/provider-switch-<ts>-<pid>/` vs `backups/weclaw-provider-migration/`）；(d) 脚本不剥离 `encrypted_content` 推理项，跨 provider resume 仍可能失败；(e) 脚本无任何文档（README/AI_CONTEXT/release 均未提及），用途与运行前提（先停 Codex App 与 WeClaw）不明。
- **证据**：`scripts/codex-provider-switch.sh:677-682, 535-539, 566, 797-818, 1007`。
- **修复**：先 `PRAGMA wal_checkpoint(TRUNCATE)` 或先 unlink sidecar 再 replace；读取源库 journal_mode 并在 staged 副本上恢复；补充 README 段落说明脚本用途、前提、两套备份目录与 Go 迁移的关系；补 rollback-after-partial-replace 与热 WAL 的测试用例。
- **工作量**：M。

---

## P2

### P2-1 微信体验（其余项）
- `summary` 模式节奏：前 40 s 两条近重复、之后静默（`messaging/progress.go:33-40, 717-740`）——初始消息已发则跳过阶段提示 0；阶段提示按 1/3/5/10 分钟时长型；ticker 取 `min(InitialDelay, Interval)`。
- `typing` 模式每 8 s 两次 HTTP（`getconfig`+`sendtyping`），缓存 `typing_ticket`；微信默认 `send_acceptance: true` 或初次延迟后发一条"收到"。
- 聚合窗口 800 ms 实际无法跨长轮询响应合并（依赖 P0-6 的 ack 解耦），合并后 `MessageID` 取最后一个导致去重漏掉——记录全部构成 ID。
- 无转写语音/视频/贴图静默丢弃、多图只取第一张（`messaging/platform_message.go:228-236`）——回复"暂不支持该类型消息"，或遍历全部附件。
- 出站附件 `os.ReadFile` 无上限（`wechat/media.go:26-32`）——stat 后 25–50 MiB 上限并提示；Markdown 图片语法引用的本地绝对路径既不发送也不提示——路径提取兼容 Markdown 图片/链接目标。
- 401/403 当普通瞬时错误无限退避（`ilink/client.go:186-188`）——按 `-14` 的 60 s fatal 路径处理并明确日志；`doctor` 加在线 `getupdates` 探测；尊重服务端 `longpolling_timeout_ms`。
- 英文/原始错误到用户（`incoming_attachments.go:194,200,213,216`、`platform_message.go:310`）与全英文 `wechat login` 流程——中文化。
- 帮助/`/mode` 文案在微信上仍说"按钮""审批卡片"——按 `Capabilities().Streaming` 分支。
- 图片-only 回复会先发一条空文本气泡（`reply_delivery.go:131-132`、`wechat/replier.go:60-71`）——转换后为空即早退。

### P2-2 飞书遗留
- `/cx` 与 `/cc` 语义不对称（裸 `/cc` 切换 Agent、`/cc 2` 发给 Claude）——统一为纯命令命名空间，消息路由只走 `@cx`/`@cc`，切换回复注明如何切回。
- 一次看清 Agent/工作空间/会话/模型需 4 条命令——`/status` 合并输出 `Agent · 工作空间 · 会话 · 模型 · 推理 · 审批模式 · 当前任务`。
- 终态卡收起预览漏掉最后一批节流中的进度（`feishu/stream.go:905, 933-952`）——`prepareDurableTerminal` 用 `latestTaskSnapshot` 重算 preview，或合并 `pendingPresentation` 再取消。
- 非用户取消（如 `task_timeout_seconds`）时卡片显示"已停止"但结果卡为"失败"（`messaging/progress.go:574-575, 622-624`）——卡片状态只由调用方 `failed/stopped` 决定。
- reference payload 不含 `WaitingApprovals`，重启后"等待私聊审批"行消失——补字段。
- `/feishu users pending` 允许在群聊执行并打印授权码（`messaging/feishu_identity_commands.go:38-60`）——要求私聊或隐藏授权码。

### P2-3 Terminal outbox 健壮性
- 每次进度 tick 全量重写并 fsync 整个 outbox，reference 内容重复存两份，多任务并发时多 MB fsync 且在 `streamMu` 内（`terminal_outbox.go:1031-1066`；`stream.go:670-673`）——去重 `Content`，reference 刷新按 sequence 变化或 ≥1 s 去抖。
- `deferDelivery`（`attach_not_ready`）每 2 s 持久化且不计 `Attempts`，永不 dead-letter——按次数或时长上限转 dead-letter 并给独立 reason。
- follower guard `discard` 不写 trace 事件（`terminal_outbox.go:1783-1793`）——补 `terminal.delivery.discarded`。
- `stageReservationResult` 失败时 `releaseReservation` 让 worker 立刻按"任务已中断"恢复，与 legacy 投递竞争（`reply_delivery.go:511-516`）——改为 `discardReservation`。
- 全部 delivered 但未 `removeDelivered` 的僵尸条目永不清理（`terminal_outbox.go:2069-2092`）——load 与 status 时视为可删。
- `beginAttempt` 不检查 `startupHeld`/`preparing`，与 AI_CONTEXT §104 "三层阻止"不符——补检查。
- 老 dead-letter 在平台 UUID 去重窗口过期后 redrive 会重复发送——status 显示条目年龄并在 redrive 时告警。
- `OpenStream` 成功到 `reserve` 落盘之间崩溃留下孤儿卡（C1/C8）——用预生成 ID 在 `OpenStream` 前先落一条"卡片待创建"reservation。
- `progressSession.deliveryGuard`、`s.reply` 存在跨锁读写竞态（`task_state.go:407`；`progress.go:1271, 164, 292, 967`）——通过 setter/原子读写统一。

### P2-4 Agent 运行时（其余项）
- 原始协议/子进程文本到达用户：`agent error: <rpc message>；<stderr>`、`ACP runtime read error: bufio.Scanner: token too long`（`ErrACPFrameTooLarge` 未被 `friendlyAgentError` 识别）、`turn error: %s` 断链等——`friendlyAgentError` 增加 `ErrACPFrameTooLarge`、`ErrCodexTurnTerminal`、`ErrCodexWriterBusy`、`ErrCodexRuntimeConflict`；`%s` 改 `%w`；JSON-RPC 错误包装为 `*rpcError` 供 `errors.As`。
- turn 状态竞态与 Desktop active-writer 恢复依赖上游错误子串匹配（`codex_runtime_turn.go:300, 425-443`）——优先匹配 `rpcError.Code`，子串作回退并加固定 fixture 测试。
- Host 断线不通知 thread observer（只遍历 `turnCh`）——把 `interrupted` 控制事件也广播到 observer mailbox；重连后在 reconcile 分支重新检查订阅。
- 官方 daemon 停止验证不确认进程退出（`codex_daemon_host.go:619-657`）——补 `waitCodexHostProcessExit`。
- 无锁 attach 可在 launcher "attach→写 metadata" 窗口观察到 metadata 缺失（`codex_app_server_host.go:65-76, 153-179`）——先写 metadata 再 attach。
- shared 模式子进程在 metadata/wait/preflight 失败时不被停止，留下 `running` 元数据孤儿（`codex_app_bridge.go:499-525`）。
- `turnIDCh <- turnID` 无保护发送（`codex_app_server_turn.go:136`）——`select`/default。
- 读循环在 `wireDispatchMu` 内做磁盘 I/O（归档通知 → `persistState`）——分发后再释放锁或移出。

### P2-5 CLI 与运维
- `doctor` 输出中英混杂、多数 warn 无下一步动作、`allow_users` key 名写错（`cmd/doctor.go`；`cmd/doctor_platform.go:79, 92`）——统一中文，每条 warn/fail 以 `运行 weclaw …` 结尾；飞书 bot 增加在线凭证校验并提示事件/回调未验证。
- `--force`/`--stop-conflicting-codex-hosts` 升级阶梯无 `Long:` 说明；后者只在 `restart` 上有；`update --force` 不带 `--restart` 被静默忽略（`cmd/restart.go:21-32`；`cmd/stop.go:18`；`cmd/update.go:26-27`）——三命令加 `Long:` 三行阶梯说明；`stop`/`update --restart` 补该 flag；拒绝无 `--restart` 的 `--force`。
- `weclaw codex cli --help` 会尝试启动 Codex（`cmd/codex_cli.go:21-26`）——单独 `-h/--help` 时拦截并展示说明。
- `install.sh` 重跑覆盖运行中二进制无提示、无降级保护、不提示 PATH、每次进 `doctor --fix` 向导、中英混排——检测已安装并提示 `weclaw update`；PATH 检查；统一语言。
- `service/weclaw.service` 描述漏飞书、`ExecStart` 硬编码；README 无 systemd 安装/日志说明。
- `weclaw trace` 文本输出不打 `trace_id`；日志 20 MiB×3 轮转未文档化；缺"机器人为什么不回"的单命令诊断——文本行加短 trace-id；文档补轮转；考虑 `status --verbose` 折叠最近失败与 outbox 积压。
- `config` 包错误全英文、`cmd` 约 70% 中文、包装后单行混合——统一中文。
- 未文档化：`companion`、`doctor processes`、`wechat send`、`feishu users approve|revoke|rename`、`web --no-open`、`start --api-addr`（默认 18011）、`/api/send` 用法、`WECLAW_HOME`、安装脚本环境变量——新增 CLI 参考与 HTTP API 小节。
- `docs/AI_CONTEXT.md:66` 仍以隐藏命令 `feishu login/bootstrap` 为入口——改为 `feishu add`；隐藏命令运行时打印兼容提示。

### P2-6 其他安全纵深
- Web 面板 Origin 校验不检查 Host 是否 loopback（`web/server.go:156-173`）；`Validate()` 允许空 token——复用 `api/auth.go` 的 loopback Host 检查，强制 token 非空。
- `authorizeRead`/`authorizeLocalControl` 不走 `auththrottle`（`api/auth.go:14-27, 51-61`）——统一走 `sendAuth`。
- 配置版本指纹哈希了含 `api_token`/`api_key` 的完整配置并返回浏览器（`web/view.go:114-124`）——哈希脱敏视图。
- remotefetch 错误串把解析出的内网 IP 透给聊天用户（`internal/remotefetch/remotefetch.go:220` → `incoming_attachments.go:194`）——用户侧通用文案，细节进日志。
- 用户上传图片 `0755/0644` 落盘（`incoming_attachments.go:198`；`artifact_files.go:60`）——`0700/0600`。
- `codexauth` 崩溃遗留的明文 `.weclaw-secure-*.tmp` 无清理（`codexauth/secure_file.go:64-69`）——`cleanupOrphanFileSecrets` 顺带清理。
- trace 文件一行损坏导致整文件 `Query` 失败（`observability/store.go:291-294`）——跳过并计数；追加前检查末尾换行。
- `sudo` 包装缺 `--` 分隔符（`agent/run_as_user.go:34-40`）。
- 非 Unix 构建下权限/owner 校验为空实现且 `0o700` 判断在 Windows 恒失败（`observability/secure_io_other.go`、`codexauth/*_other.go`）——当前 `GOOS=windows` 本就编译失败，改为显式 "unsupported" 与 `internal/securefile/lock_other.go` 一致。
- WeChat 授权码存储满 1024 条即拒绝新用户（`messaging/access_code.go:97-104`）——淘汰最旧而非拒绝。

---

## P3 工程健康

- **拆分**：`messaging/terminal_outbox.go`（2458 行）按 模型+校验 / 持久化 / 投递循环 / 状态渲染 拆分；`messaging/progress.go`、`feishu/stream.go`、`agent/codex_host_conflict.go` 同理。
- **复杂度**：`reconcileExternallyProjectedCodexAccount`（CC 70）与 `UseCodexAccount`（CC 53）抽公共 `accountSwitchTxn{begin,verify,commit,rollback}`；`codexSessionStore.load`（CC 53）对齐 Claude 侧 `decode`+`normalize` 结构；`PrepareCodexRestartWithOptions`（CC 50）按 force/普通/stop-conflicting 拆策略函数。
- **平台抽象泄漏**：`messaging/` 30 处 `platform.PlatformFeishu ==` 比较、21 个 `feishu_*.go` 文件；元数据 key 字面量在 `feishu/` 与 `messaging/` 各写一份（`feishuSessionMetadataKey` 定义两次）——key 常量移到 `platform`，能力判断改用 `platform.Capabilities`。
- **重复**：`lockCodexSessionThreads` vs `lockClaudeSessionControls`（锁排序算法字节级相同，是死锁一致性风险）、`sortedUniqueCodexThreadIDs` vs `sortedUniqueClaudeSessionIDs`、`loadFeishuCodexWorkspaceSnapshot` vs `loadFeishuClaudeWorkspaceSnapshot`——泛化为 `lockOrdered`/`sortedUniqueIDs`/`feishuWorkspaceChoiceSpec`。
- **死代码**：`agent/codex_app_server_gate.go:124-236` drain/restart 半边 + `codex_runtime_recovery.go:13-37`；`feishu/config.go:41-184` 单 bot 凭证 API；`config.LegacyAllowedWorkspaceRoots`（若 P1-7 不复用则删除）；约 60 个仅测试调用的生产端包装函数迁到 `_test.go`。
- **测试与 CI**：57 处 `time.Sleep` in tests，套件 CPU 利用率 42%；`messaging` 在 CI `-race -timeout 180s` 下约 38 s——注入时钟/channel 替换前 3 个文件的 sleep；12 个 `//go:build darwin` 文件（1362 行）在 ubuntu-only CI 中从不编译——增加 `macos-latest` job（`go build ./... && go test ./agent -run 'Darwin|Daemon|Desktop'`）；`"cli"` Agent 类型 3 处接受、2 处迁移/拒绝——统一。
- **覆盖缺口**（来自 agent/ 审查）：Stop 与进行中 Start 的竞争；10 s stdin `SetWriteDeadline` 路径；响应在 `OnTurnStarted` 前丢失；managed 模式真实 SIGINT/SIGKILL 分支；跨断线/重连的 pending 交互；账号切换步骤 8/10/11/13 的失败注入；`verifyCodexDaemonStopped` 遇仍存活进程；messaging 层把 `agent error:`/`turn error:`/`ACP runtime read error:` 喂给 `friendlyAgentError` 的测试。
- **文档可维护性**：`docs/AI_CONTEXT.md` 17 个 bullet 超 1000 字（最长 5668 字）——按子标题拆为单事实 bullet；`scripts/validate_docs.py` 增加 bullet 长度告警（>600 字）；README_CN 与 README 结构对齐（飞书菜单段落、打包发布小节）。
- `Makefile` 增加 `check` 目标覆盖 AI_CONTEXT 列出的六条验证命令。

---

## 执行顺序建议

| 批次 | 内容 | 预计 |
|---|---|---|
| 第 1 批 | P0-1、P0-2、P0-4、P0-5（短期）、P0-8、P0-9 | 各 S，合计 2–3 天 |
| 第 2 批 | P0-3、P0-6、P0-7 | 合计 3–4 天 |
| 第 3 批 | P1-1、P1-2、P1-9（短期）、P1-10、P1-11、P1-12、P1-13、P1-14 | 合计 3–4 天 |
| 第 4 批 | P1-3、P1-4、P1-5、P1-15、P1-16 | 合计 1–1.5 周 |
| 第 5 批 | P1-6、P1-7、P1-8（信任模型三件套，需一起设计） | 1–1.5 周 |
| 之后 | P2 按迭代排入；P3 与功能开发交错 | 持续 |

每批完成后运行 `docs/AI_CONTEXT.md` 列出的 full 验证集，并对涉及飞书/微信真实端的改动做一次真机验收。

## 已核实为非问题（避免重复排查）

- `/api/send` 不能发送本地文件（`remotefetch` 只允许 http/https、拒绝私网/特殊地址、拨号时重新解析 DNS、重定向重新校验、25 MiB 上限）。
- `app_secret`/token 从不进日志、trace、`config.json`；Web 视图用 `__WECLAW_UNCHANGED__` 掩码。
- 用户正文从不写日志或审计，只记 rune 数。
- 审批与 YOLO 按（操作者, route）隔离，卡片点击使用飞书 `Operator.OpenID`，群成员无法替他人批准或开启 YOLO。
- `IncomingMessage.accessGrant` 不可导出且绑定平台+账号+身份，不可伪造。
- 审批面板 `CreateCard` 失败返回 `false, nil` 后会回落到独立选择卡，不是静默丢失。
- 结构化进度走 `updatePresentationWithSequence` + `visibleCardContent(snapshot)`，展开状态不会被重置。
- `withApprovalCardOperation` 重试循环不会无限自旋（`moveApprovalWaiting` 的 `approvalSuccessor` 守卫阻止环）。
- 读循环内没有任何 RPC 调用；所有从读路径发出的 channel 发送均为 `select`/default 或有界。
- `reapACPProcess` 是 stdio ACP 唯一的 `Cmd.Wait`；启动 leader 使用 `context.WithoutCancel` + 2 分钟上限。
- 全部 13 处 `context.WithoutCancel` 用法均有正当理由且后续重新加了有界超时。
