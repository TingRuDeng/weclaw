---
ai_summary:
  purpose: "记录 2026-07-13 对 WeClaw 全仓库的深度审查结论：缺陷清单、安全姿态、架构健康度、测试边界与修复优先级。"
  read_when:
    - "修复本报告列出的缺陷或复核其状态时。"
    - "重构 Handler、平台抽象或外部 Codex 任务生命周期前评估风险时。"
    - "需要了解测试安全网边界或安全防线现状时。"
  source_of_truth:
    - "messaging/handler.go"
    - "messaging/codex_external_task.go"
    - "feishu/adapter_events.go"
    - "api/auth.go"
    - "platform/access.go"
  verify_with:
    - "python3 scripts/validate_docs.py . --profile generic"
    - "git diff --check"
  stale_when:
    - "本报告列出的缺陷被修复或相关文件重构后。"
    - "新增平台、Agent 类型或安全边界变化后未重新审查。"
---

# WeClaw 深度审查报告（2026-07-13）

## Purpose

记录 2026-07-13 对 WeClaw 全仓库的一次性深度审查结论，供后续修复缺陷、评估重构风险和复核安全边界时查阅。本文是时点快照，不承担持续维护的架构事实（那是 `docs/AI_CONTEXT.md` 的职责）。

## Source of truth

- 审查基准：commit `9b42cda`（修复：阻止飞书历史消息重放），工作区无代码改动。
- 缺陷引用的权威位置以各条目标注的源码文件为准：`messaging/codex_external_task.go`、`feishu/adapter_events.go`、`messaging/linkhoard_save.go`、`messaging/agent_broadcast.go`、`messaging/message_dedup.go` 等。
- 审查方法：五个维度并行深度审查（安全与访问控制、并发安全、逻辑正确性、架构与可维护性、测试工程），叠加对关键安全路径的独立人工核查与全套自动化检查。所有缺陷均在报告落笔当日于当前代码上逐条复核确认仍然存在。

## Key facts

本次审查确认了 7 个主要正确性或并发问题，其中外部 Codex watcher、飞书附件失败和任务排队竞态需要优先处理；广播路径还存在一处可达的未同步读写。已核查的访问控制、API 鉴权和远程抓取边界具备系统性防护，但本报告不把有限范围的静态审查外推为全仓安全证明。

## 自动化基线（全部通过）

- `go build ./...`、`go vet ./...`：通过。
- `staticcheck`（`all` 级别，仅排除 ST1000/ST1005 两项风格检查）：零告警。
- `go test ./... -count=1`：13 个包中有 12 个包含测试；基准提交包含 1176 个顶层 `Test*` 函数，Darwin 构建选择 1175 个。单次通过不能证明不存在 flaky。
- `go test -race ./messaging ./agent ./cmd`：无 DATA RACE（注意：race 检测只覆盖被测试触发的交错，不能证明无竞态，见缺陷 4）。
- `python3 scripts/validate_docs.py . --profile generic`、`git diff --check`：干净。
- 原审查未保留可复核的 coverage profile，因此不把包级覆盖率百分比作为本报告的权威基线。

## 缺陷清单（按严重度）

### 高 1：飞书附件下载失败后当前投递被静默终止

位置：`feishu/adapter_events.go`（`handleMessageEvent` 中 `attachMessageResources` 失败分支）配合 `feishu/incoming.go`（`toIncomingEnvelopeFromMessage` 内 `deduper.isDuplicate` 提前记账）。

去重记账发生在解析阶段：event ID / message ID / 内容指纹立即写入 seen 表并持久化到磁盘；附件下载在其后执行，失败时只打日志 `return nil`，不给用户任何回复。用户发送超过 32MiB 的文件（`feishu/incoming.go` 有硬性大小限制）必然触发。去重记录的 TTL 为 10 分钟，因此相同事件在该窗口内及进程重启后仍会被直接忽略；不能把这一行为表述为无限期去重。

修复方向：永久错误应通过 scoped replier 返回明确提示并完成消费；临时错误应由当前处理所有者安全释放占位并允许重试。不能简单把记账整体后移，也不能无条件删除可能由其他并发处理者持有的 key。

### 高 2：外部 Codex 任务 watcher 非终态退出不清理 activeTasks，会话被幽灵任务永久卡死

位置：`messaging/codex_external_task.go`（`runExternalCodexTaskWatcher` 的 `if !result.Terminal { ...; return }` 分支）。

非终态返回时任务不从 `Handler.activeTasks` 删除、`done` 不关闭。至少三条可达路径：

1. `/stop` 打在未实现 `CodexThreadRuntimeAgent` 的纯 CLI 型 agent 的外部 rollout watcher 上——`cancelActiveTask` 只取消 ctx 不删条目，watcher 判非终态早退。
2. rollout 文件被删或读失败——无需任何用户操作。
3. 被观察的 turn 被 Codex App 内新 turn 顶掉——watcher 以 200ms 间隔无限轮询（goroutine 泄漏），永远等不到匹配的 TurnID。

后果：第一条后续消息可能进入无法提升的 pending，更多消息会因 pending 已占用而被拒绝。只有 `/stop` 已把任务标记为 detached 的路径会从 `/ps` 隐藏；其它异常路径仍可能显示。部分断线场景可随 rollout 恢复，不应统一表述为只能重启。

修复方向：明确区分用户取消、临时断线、不可恢复文件错误和 turn 被替换。临时断线继续观察；确定终态通过统一收尾路径清理任务、关闭 `done`、反馈失败并提升 pending。不能对所有非终态无条件调用 `finishActiveTask`。

### 中 3：网页标题按字节截断产生非法 UTF-8 文件名

位置：`messaging/linkhoard_save.go`（`sanitizeFileName` 的 `result[:200]`）。

标题来自网页 title，中文通常每字 3 字节；纯三字节汉字从第 67 个开始可能在 200 字节截断点产生非法 UTF-8。代码能够证明非法序列会产生，但本次审查没有实测并确认 macOS/APFS 的具体错误文本。该路径会向用户返回保存失败，不涉及静默覆盖，因此评为中等级。

修复方向：在 UTF-8 rune 边界内控制最大字节数，并为 `.md`、`.sidecar.md` 和冲突序号预留长度；不能直接改成保留 200 个 rune。

### 中 4：广播路径无锁写 wechat.Replier.ClientID，真实数据竞态

位置：`messaging/agent_broadcast.go`（`sendBroadcastAgentResult` 中 `wxReply.ClientID = NewClientID()`）。

`wechat/replier.go` 的 `clientIDsForTextChunks` 在 `r.mu` 下读写同一字段；广播时多个 agent goroutine 共用同一底层 Replier，一个 goroutine 仍在发进度（读 ClientID）时主循环无锁写入。现有 `-race` 测试未触发此交错故未报。`messaging/handler.go` 中的同类写是安全的（每条入站消息新建独立 Replier），仅广播路径有问题。

修复方向：把 ClientID 轮转收进 serializedReplier 的锁内，或干脆不重设（SendText 对后续 chunk 自会生成新 ID）。

### 中 5：文本去重误伤合法重复消息，且回复文案与事实不符

位置：`messaging/message_dedup.go`（`isDuplicateTextMessage`）配合 `messaging/handler.go` 的调用点。

微信消息无 message_id 时按"用户+上下文+归一化文本"去重（TTL 默认 300s），不检查该会话是否真有任务在运行，任务完成后也不清除 key。用户 5 分钟内重发同样的"继续"（完全合法）会被吞，且回复称"这条任务已经收到，正在处理中"——实际没有任务在跑，承诺的结果永远不来。

修复方向：判重前检查 `activeTask` 是否存在，或任务终态时清除对应 key；至少把文案改为中性表述。

### 中 6：保存目录创建失败时用户无任何反馈

位置：`messaging/incoming_attachments.go`（`handleImageAttachmentSave` 中 `os.MkdirAll(h.saveDir, ...)` 失败分支）。

同函数内下载失败、写文件失败都会通知用户，唯独 MkdirAll 失败只打日志静默返回。saveDir 不可写（权限变更、磁盘只读）时用户发纯图片完全无响应。修复方向：与同函数其它分支一致，补发失败提示。

### 中 7：beginActiveTask 与 storePendingGuide 之间的窗口导致消息丢弃并误导用户

位置：`messaging/codex_agent_task.go`（`beginActiveTask` 返回未启动后到 `storePendingGuide` 之间）；同型窗口亦存在于 `messaging/codex_task_start.go` 的 `queueMessageBehindLiveTask`。

新消息拿到"任务运行中"判定后、暂存前，任务恰好收尾并从 map 删除，暂存重查失败，用户收到"当前任务已有一条暂存消息"的错误提示——该消息既没排队也没执行。修复方向：让一次原子操作在同一 `activeTasksMu` 临界区内完成“启动或排队”，并区分 `started`、`queued`、`task_missing`、`pending_occupied`。同型窗口还存在于 Claude 后台任务和 Codex 广播入口，必须一并处理；简单重试一次不能闭合全部窗口。

### 低（择要）

- `messaging/approvals.go`：同一微信用户并发多个审批时，文本回复"允许"因歧义被有意忽略但无任何提示，审批全部超时拒绝且回复被排成新任务。建议歧义时回复"有 N 个待确认请求，请指定"。
- `messaging/codex_feishu_cards.go`：用"失败/不存在"等中文子串嗅探命令回复是否为错误，目录名含这些词时误判（仅降级为纯文本，功能可用）。建议改结构化成功/失败标志。
- `messaging/handler.go`：saveDir 配置后，以 URL 开头的消息整条被 linkhoard 吞掉——"链接 + 分析指令"的指令部分静默丢弃。建议 URL 后还有文字时改走 agent 或在回复中说明。
- `messaging/task_state.go`：`restorePendingGuide` 回滚窗口内任务被移除时暂存消息丢失（用户已收到失败反馈，影响有限）。
- `messaging/admin_commands.go`：管理命令 5 分钟超时在等待 `serviceAdminMu` 期间就开始计时，前一条命令占锁近 5 分钟时后一条拿到锁即超时。建议把 WithTimeout 挪到 Lock 之后。
- `messaging/message_dedup.go`：TTL 过期边界上两条并发同文本消息可双双通过（窗口极小，尽力而为层）。
- `messaging/handler_config.go`：`SetSaveDir` 是唯一无锁 setter，当前仅启动期调用故不构成缺陷，但与其余 setter 风格不一致，若纳入热重载即成竞态。建议补锁或注释声明。
- `api/server.go`、`api/send.go`：`json.NewEncoder(w).Encode` 返回值被忽略，仅客户端断开时丢响应，建议记日志。

## 安全姿态：已审查边界内未发现高中危漏洞

以下防线经独立核查确认到位。API `/health`、Web 首页和静态资源是未鉴权的设计例外；SSRF 结论只覆盖使用 `internal/remotefetch` 的链路，不代表所有出站 HTTP 都具备相同防护。

- **API/Web 鉴权**：`crypto/subtle` 常量时间 token 比较（`api/auth.go`）；`isTrustedLoopbackRequest` 校验 Host 为 loopback 抵御 DNS rebinding；非 loopback 监听强制 token（`api/server.go` 与 `web/server.go` 的 Validate）；请求体 1MiB 限制、拒绝多 JSON 值；多账号强制 `account_id`（`api/send.go`）。`isLoopbackListenAddr` 对 `0.0.0.0`、`[::]`、空地址等均正确判为非 loopback。
- **访问控制**：空 `allowed_users` 默认拒绝（`platform/access.go`）；消息在 `guardedDispatch` 层即被拦截；匹配为 TrimSpace 后精确匹配，无大小写折叠或前缀匹配；管理命令额外校验 `admin_users`，飞书管理员身份只接受 `union_id`（`messaging/admin_commands.go`），避免跨应用 open_id 伪造；未授权飞书身份仅记录不放行。
- **命令注入**：所有外部进程均 `exec.CommandContext` 显式传参，无 shell -c；RunAsUser 用 `sudo -n -u` 数组传参且环境变量白名单剔除含 `=` 的键；sqlite 查询有正确的单引号转义（`messaging/codex_app_sessions.go` 的 `sqliteString`）；osascript 参数双重转义。
- **路径穿越**：入站文件名经 `filepath.Base`；工作区限定用 `filepath.Rel` + `EvalSymlinks`（`messaging/attachment.go`），非前缀字符串匹配。
- **SSRF**：`internal/remotefetch` 校验协议、拒绝私网/链路本地/保留网段，并在拨号时（`safeDialContext`）重新解析校验 IP 击败 DNS rebinding，重定向逐跳校验。
- **凭证**：飞书 `app_secret` 只存独立 0600 文件，配置结构体无 secret 字段；web 面板密钥掩码、永不回显；审计日志排除密钥与消息正文；更新流程校验 release sha256。

两条低危备查（均需错误配置才有意义）：

1. API 发送端无失败限速——web 面板有 `authThrottle`（10 次/分钟封禁），API 没有对应机制。仅当 `api_addr` 显式绑定非 loopback 且使用弱 token 时可被在线暴力猜测。建议复用 authThrottle 思路。
2. web 的 `sameOrigin` 只校验 Origin 与 Host 相等，不校验 Host 为 loopback，弱于 API 同类实现；当前因 web 启动总是生成 24 字节随机 token 而不可利用，属纵深防御不对称。建议与 API 对齐。

## 架构健康度

亮点：platform 抽象真实有效（飞书这个最复杂的平台零泄漏进业务层——messaging 非测试代码零引用 feishu 包）；依赖无环且有守护测试（`messaging/dependency_test.go`）；旧命令移除带防回归测试（`messaging/command_compat_removal_test.go`）；legacy 配置直接报错拒绝而非静默迁移；文件与函数粒度全面健康（最大非测试文件约 300 行）。

三笔明确的债：

1. **Handler 上帝对象**：`messaging/handler.go` 的 Handler 有 47 个字段、341 个方法散布在 64 个文件，直接持有 6 个 `sync.Mutex`/`sync.RWMutex` 字段。建议按锁域渐进拆出 TaskManager（taskLocks + activeTasks + approvals）和 SessionService（codex/claude/agent sessions），先让新功能停止往 Handler 加字段。
2. **wechat 的制度化豁免**：依赖守护测试禁止 messaging 引用 feishu，却漏了 wechat——业务层有 6 处直接引用 wechat 包（`*wechat.Replier` 类型断言、ChunkRunes、SendMediaFromURL、CDN 下载默认值），归纳为 4 种能力，platform 已有可选接口模式（TaskCardReporter 等）可照搬。改完后把 `weclaw/wechat` 加进禁令，兑现"业务层平台无关"的承诺。
3. **agent 生命周期仍有多套入口**：default 与 named 已统一调用 `dispatchAgentMessage`，不存在两条逐行同构的执行链；broadcast、Codex 后台和 Claude 后台仍维护各自的生命周期变体。`isCodexAgent` 在 messaging 有 14 个生产调用点，后续应围绕任务登记、进度和终态收尾继续收敛，而不是重复合并已统一的 default/named 路径。

次要债：claude 侧复用 codex 命名的类型（`codexWorkspaceView` 等），建议改中性名；progress 配置合并顺序是 agent 覆盖先应用、平台/账号覆盖后应用（`messaging/progress_config.go`），与"越具体越优先"的直觉相反且无注释；Capabilities 10 个字段仅 4 个被业务层消费，`FinalReplyOutsideStream` 等命名泄漏了飞书行为细节；仅测试调用的包装函数（`HandlePlatformMessage`、无 Account 版 sendTo 系列）建议清理；`config.BuildAliasMap` 保留字表仍含已删除的 `info`/`clear`。

## 测试评估：可信的安全网，但边界要清楚

测试包含真实 HTTP 行为测试、安全负例以及大量 fake/stub 和直接 handler 测试；没有分类统计支持“以真实 HTTP 线路为主”的原结论。安全闸门覆盖 DNS 地址校验、跨 Origin、workspace 越权拒绝等负例，全局状态相关 helper 普遍先用 `t.Setenv` 切换到临时 HOME。

安全网的洞在边缘层，重构以下区域前需先补测试：

- `internal/remotefetch`：SSRF 运行时拨号路径几乎未测（`safeDialContext` 覆盖约 11%，`resolveSafeIP`/`Download` 为 0%）。
- `web/handlers.go`：配置面板全部写操作 0%（含写飞书 AppSecret 的 handler）。
- `wechat/cdn.go`：CDN 加解密与上传下载全部 0%。
- `config/persistence.go`：`Load` 没有本包直接测试，坏文件路径未覆盖；部分飞书身份命令测试会间接调用该入口，不能表述为真实入口从未被测。
- `messaging/attachment.go`：`EvalSymlinks` 防御代码没有任何 symlink 逃逸测试。
- `messaging/feishu_identity_commands.go`：`ApproveFeishuIdentity`/`RevokeFeishuIdentity` 公开入口 0%。

最值得立刻补的三个测试：remotefetch `safeDialContext` 行为测试、attachment symlink 逃逸用例、`config.Load` 坏文件容错。

## 建议的行动顺序

1. **优先修复**：高 2（watcher 状态分类与终态收尾）、高 1（失败反馈与有所有权的去重状态）、中 7（原子启动或排队）、中 4（ClientID 竞态）。
2. **正确性修复**：中 5（文本去重与任务生命周期挂钩）、中 3（UTF-8 边界内按字节截断）、中 6（MkdirAll 反馈）、`restorePendingGuide` 回滚窗口。
3. **架构层**（渐进）：封装 wechat 平台能力 → 收敛后台与广播任务生命周期 → Handler 按锁域拆分；每步之前给对应区域补测试（优先 safeDialContext、symlink 逃逸、config.Load）。

## How to verify

quick:

```bash
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

full:

```bash
go test ./... -count=1 -timeout 180s
go vet ./...
go test -race ./agent ./cmd ./messaging -count=1 -timeout 60s
```

## Stale when

- 本报告列出的任一缺陷被修复，或其所在文件发生重构。
- 新增平台、Agent 类型、命令入口或安全边界后未重新审查。
- Handler 拆分、wechat 依赖豁免或执行管道合并等架构项落地后。
