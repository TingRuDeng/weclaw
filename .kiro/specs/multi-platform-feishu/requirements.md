# Requirements Document

## Introduction

weclaw 当前是把微信桥接到 AI 编程 agent（Codex / Claude / Gemini 等）的 Go 服务，传输层（`ilink` 包）与核心业务逻辑（`messaging` 包）深度耦合。本特性引入**平台抽象层**解耦传输与业务，新增**飞书（Lark）接入并一步到位支持卡片 / 流式 / 交互按钮**，并借鉴参考项目 open-im 对**微信接入做针对性增强**。

本需求文档从已确认的 `design.md` 反推。以下 4 项决策已由用户确认并在本文档中固化：

1. **访问控制默认策略 = 拒绝所有（fail-safe）**：`allowed_users` 为空时拒绝所有用户，启动时醒目告警。
2. **飞书仅支持单聊（P2P）**：不支持群聊。
3. **消息聚合默认开启，窗口 800ms**：可配置，可设 0 关闭；命令类消息不参与聚合。
4. **保留独立 `ilink` 包**：`wechat` adapter 封装现有 `ilink`，不重命名 / 合并。

实施分 4 阶段（阶段 1 抽象重构 / 阶段 2 飞书 MVP / 阶段 3 飞书富交互 / 阶段 4 微信优化），每条需求标注其归属阶段。每阶段以 `go build/vet/test ./...` 全绿为验收门槛。

## Glossary

- **平台（Platform）**：一个 IM 接入端（wechat / feishu），实现统一抽象接口。
- **Replier**：针对单条入站消息所在会话的回复句柄，封装平台发送细节。
- **Capabilities**：平台能力声明（text / typing / image / card / streaming / buttons / longtext）。
- **ConversationKey**：跨平台唯一会话路由 key 前缀，形如 `"<platform>:<userID>"`。
- **CardKit**：飞书卡片 2.0 流式更新能力。

---

## Requirements

## Requirement 1: 平台抽象接口（阶段 1）

**User Story:** 作为 weclaw 维护者，我希望核心业务逻辑面向平台无关的抽象编程，以便新增 IM 平台时无需侵入 `messaging` 核心逻辑。

#### Acceptance Criteria

1. THE SYSTEM SHALL 在新增的 `platform` 包中定义接口 `Platform`、`IncomingMessage`、`Replier`、`Stream`、`Capabilities`、`Registry`、`AccessControl`。
2. THE `platform` 包 SHALL NOT import `messaging`、`ilink`、`feishu` 或任何 `lark*` SDK 包（避免循环依赖与传输细节泄漏）。
3. WHEN `messaging` 处理一条消息 THE SYSTEM SHALL 通过 `HandleMessage(ctx, platform.IncomingMessage, platform.Replier)` 接收，而不再直接依赖 `*ilink.Client` 或 `ilink.WeixinMessage`。
4. THE `messaging` 包 SHALL NOT 直接 import `ilink`、`feishu` 或 `lark*`；其平台相关行为只通过 `platform` 抽象访问。
5. THE `Platform` 接口 SHALL 暴露 `Name()`、`AccountID()`、`Capabilities()`、`Run(ctx, DispatchFunc) error`。
6. THE `Replier` 接口 SHALL 暴露 `Capabilities()`、`SendText`、`SendImage`、`Typing`、`OpenStream`、`AskChoices`。
7. WHEN 某平台不支持 `Replier` 的某能力 THE SYSTEM SHALL 降级为受支持能力（如文本）或安全 no-op（如 typing），并且 SHALL NOT panic 或向业务层返回未处理错误。

_Validates: Property 1（平台解耦不变量）、Property 3（能力降级安全）_

---

## Requirement 2: 微信 adapter 重构与零回归（阶段 1）

**User Story:** 作为现有微信用户，我希望抽象重构后微信行为完全不变，以便升级无感。

#### Acceptance Criteria

1. THE SYSTEM SHALL 保留独立的 `ilink` 包（传输 / CDN / 扫码），新增 `wechat` adapter 包封装 `ilink` 并实现 `platform.Platform`。
2. THE SYSTEM SHALL 将原 `messaging/sender.go` 的微信文本处理（`MarkdownToPlainText`、`FormatTextForWeChatDisplay`、`splitTextReplyChunks` 的 1800 runes 分段）下沉到 `wechat` adapter 的 `Replier` 实现，且对相同输入产出相同的微信分段 / 换行 / 纯文本结果。
3. THE SYSTEM SHALL 将原 `messaging/progress.go` 的 typing / 末段文本预览逻辑改为通过 `Replier.Typing` 与 `Replier.OpenStream → Stream` 实现，且微信能力下的行为与现有进度行为等价。
4. WHEN 微信收到文本 / 语音 / 图片 / 文件消息 THE `wechat` adapter SHALL 完成文本抽取、语音转写、附件下载，并产出已清洗的 `IncomingMessage.Text` 与 `IncomingMessage.Attachments`。
5. THE SYSTEM SHALL 使所有既有测试（`ilink/monitor_test.go`、`messaging/handler_test.go`、`messaging/progress_test.go`、`messaging/media_test.go`、`messaging/attachment_test.go`、`messaging/codex_sessions_test.go`、`api/server_test.go`、`cmd/start_test.go`）在重构后全部通过。
6. THE SYSTEM SHALL 使 `go build ./...`、`go vet ./...`、`go test ./...` 在阶段 1 完成后全部通过。
7. THE `wechat` adapter SHALL 声明 `Capabilities{Text:true, Typing:true, Image:true, File:true, Card:false, Streaming:false, Buttons:false, LongText:true}`。

_Validates: Property 8（文本分段保真）、Property 9（微信零回归）_

---

## Requirement 3: 跨平台会话路由与持久化迁移（阶段 1）

**User Story:** 作为同时使用多个平台的用户，我希望不同平台、不同用户的会话互不串话，且老的微信会话在升级后不丢失。

#### Acceptance Criteria

1. THE `IncomingMessage` SHALL 提供 `ConversationKey()` 返回 `"<platform>:<userID>"`。
2. THE SYSTEM SHALL 将 `codexBindingKey`、`buildCodexConversationID`、`buildClaudeConversationID` 中的 `userID` 入参替换为 `ConversationKey()`。
3. WHEN 两条消息的平台或 UserID 不同 THE SYSTEM SHALL 路由到不同的 agent 会话。
4. WHEN 加载旧的 `codex-sessions.json` / `claude-sessions.json` 且 key 无平台前缀 THE SYSTEM SHALL 将其视为 `"wechat:"` 前缀并回写（一次性迁移）。
5. THE 迁移函数 SHALL 幂等（对已带平台前缀的 key 不重复加前缀）。

_Validates: Property 5（会话隔离）_

---

## Requirement 4: 飞书长连接接入与凭证管理（阶段 2）

**User Story:** 作为飞书用户，我希望用 app_id/app_secret 接入 weclaw 并通过长连接收消息，无需暴露公网 webhook。

#### Acceptance Criteria

1. THE SYSTEM SHALL 引入 `github.com/larksuite/oapi-sdk-go/v3`（core / ws / im / cardkit）依赖。
2. THE `feishu` adapter SHALL 使用 `larkws` 长连接（WSClient）接收事件，订阅 `im.message.receive_v1` 与 `card.action.trigger`，无需公网入站端口。
3. THE SYSTEM SHALL 提供 `weclaw feishu login --app-id <id> --app-secret <secret>` 命令，将凭证写入 `~/.weclaw/platforms/feishu.json`（文件权限 `0600`）并校验凭证有效性。
4. THE SYSTEM SHALL 提供 `weclaw feishu status` 命令显示飞书连接 / 权限状态。
5. THE SYSTEM SHALL 支持环境变量 `WECLAW_FEISHU_APP_ID` / `WECLAW_FEISHU_APP_SECRET` 覆盖文件凭证。
6. THE SYSTEM SHALL NOT 将 `app_secret` 明文写入 `config.json`，且 SHALL NOT 在日志中打印 `app_secret`（仅可打印 `app_id`）。
7. WHEN 飞书 API 返回权限错误码（如 `99991400` 等）THE SYSTEM SHALL 识别并输出指向 `https://open.feishu.cn/app/{appId}/permission` 的开通引导（控制台日志 + 尽力发送到聊天），并 SHALL 对引导做冷却（如 60s）以避免刷屏。
8. THE `weclaw login`（微信扫码）流程 SHALL 保持不变，不与飞书凭证流程混用。

_Validates: Property 1（平台解耦不变量）_

---

## Requirement 5: 飞书单聊文本与图片收发（阶段 2）

**User Story:** 作为飞书用户，我希望在单聊里向 bot 发文本/图片并收到 agent 回复。

#### Acceptance Criteria

1. WHEN 飞书单聊收到 `text` 消息 THE adapter SHALL 解析 content 并清洗（处理 `<p>`/`<br>`/`&nbsp;` 等，保留中间空格）后产出 `IncomingMessage.Text`。
2. WHEN 飞书单聊收到 `post`（富文本）消息 THE adapter SHALL 将富文本展开为纯文本。
3. WHEN 飞书单聊收到 `image` / `file` / `audio` / `media` 消息 THE adapter SHALL 通过 `im.messageResource.get` 下载资源到本地并填充 `IncomingMessage.Attachments`。
4. WHEN agent 产生文本回复 THE adapter SHALL 通过 `im.message.create`（`msg_type=text`）发送；超出单条上限时 SHALL 拆为多条。
5. WHEN agent 回复包含图片 THE adapter SHALL 上传图片获取 image_key 后以 `msg_type=image` 发送。
6. IF 入站消息来自群聊（非 P2P）THEN THE SYSTEM SHALL 不触发 agent 处理（忽略或提示不支持单聊以外场景）。
7. THE `feishu` adapter SHALL 声明 `Capabilities{Text:true, Typing:true, Image:true, File:true, Card:true, Streaming:true, Buttons:true, LongText:false}`。

_Validates: Property 2（最终结果送达）_

---

## Requirement 6: 飞书 CardKit 流式与卡片状态（阶段 3）

**User Story:** 作为飞书用户，我希望看到 agent 回复以打字机方式流式呈现，并在完成/出错时看到清晰的卡片状态。

#### Acceptance Criteria

1. WHEN 在 `stream` 进度模式下开始一次 agent 任务 THE adapter SHALL 创建 CardKit 卡片（thinking 状态）、发送、启用流式（enableStreaming）。
2. WHILE agent 持续输出增量 THE `Stream.Update` SHALL 以节流间隔（默认约 500ms）调用 `streamContent` 增量更新卡片，且 sequence 严格单调递增。
3. WHEN agent 任务成功完成 THE `Stream.Complete` SHALL 关闭流式（disableStreaming）并将卡片全量更新为 done 状态、展示最终完整内容。
4. WHEN agent 任务失败 THE `Stream.Fail` SHALL 将卡片更新为 error 状态并展示错误文案。
5. WHEN `streamContent` 返回可忽略限流码（如 `200400`/`200810`/`300317`）THE SYSTEM SHALL 静默忽略；WHEN 返回流式失效码（`200850`/`300309`）THE SYSTEM SHALL 重新 enableStreaming 后重试一次，超过重试上限后放弃流式但仍 SHALL 保证最终结果通过 Complete 全量送达。
6. THE SYSTEM SHALL 在 Complete / Fail 后销毁（destroy）CardKit 会话资源。

_Validates: Property 2（最终结果送达）_

---

## Requirement 7: 飞书交互按钮选择端到端（阶段 3）

**User Story:** 作为飞书用户，当 AI 让我"选 1/2/3"时，我希望直接点按钮，而不是手敲数字。

#### Acceptance Criteria

1. WHEN agent 回复被 choice 检测识别为含选项（如 "1. … 2. … 3. …" + 选择提示词）THE SYSTEM SHALL 调用 `Replier.AskChoices(prompt, choices)`。
2. WHEN 在飞书平台调用 `AskChoices` THE adapter SHALL 渲染按钮卡片，每个选项一个按钮，按钮 value 含 `{action:"choice", choice:"<N>", conv:"<ConversationKey>"}`。
3. WHEN 用户点击按钮触发 `card.action.trigger` THE adapter SHALL 在 3 秒内同步返回 toast 响应，并异步把该动作归一化为 `IncomingMessage{RawCommand: CardAction{Action:"choice", Value:{"choice":"N"}}}` 重新进入处理流程。
4. WHEN `HandleMessage` 收到 `RawCommand.Action == "choice"` THE SYSTEM SHALL 把选择编号当作该会话的普通文本输入，复用既有 agent 路由路径。
5. WHEN `HandleMessage` 收到 `RawCommand.Action == "stop"` THE SYSTEM SHALL 取消该会话的 active task。
6. THE 卡片回调处理 SHALL 校验回调用户在访问控制白名单内，拒绝伪造来源。
7. WHEN 在微信平台调用 `AskChoices` THE adapter SHALL 降级为编号文本，用户回复数字即作为普通输入进入同一路由路径。

_Validates: Property 6（选择回流一致性）、Property 4（访问控制强制）_

---

## Requirement 8: 能力协商与进度模式映射（阶段 1 接口 / 阶段 3 落地）

**User Story:** 作为用户，我希望同一套进度配置（off/typing/summary/stream）在微信和飞书上都能合理工作，各自发挥平台能力。

#### Acceptance Criteria

1. THE 进度 / 渲染降级 SHALL 只发生在 adapter 的 `Replier`/`Stream` 内部；`messaging` SHALL 始终面向全能力意图编程（唯一显式分支为 choice 渲染，且由 `AskChoices` 内部消化）。
2. WHEN 模式为 `off` THE SYSTEM SHALL 不发送任何中间进度，仅发送最终结果。
3. WHEN 模式为 `typing` THE 微信 SHALL 发送 typing 心跳并最终发送完整文本；THE 飞书 SHALL 以 thinking 卡片占位并最终替换为 done 卡片。
4. WHEN 模式为 `stream` THE 微信 SHALL 以 typing + 节流末段文本预览呈现；THE 飞书 SHALL 以 CardKit 打字机增量更新呈现。
5. WHEN 模式为 `summary`（含 `verbose`/`debug` 当前等价处理）THE SYSTEM SHALL 周期性发送阶段提示。
6. FOR 任意平台与任意进度模式，WHEN agent 任务成功完成 THE SYSTEM SHALL 保证最终完整结果送达用户，且流式过程更新 SHALL NOT 替代最终态。

_Validates: Property 2（最终结果送达）、Property 3（能力降级安全）_

---

## Requirement 9: 配置模型演进与向后兼容（阶段 2）

**User Story:** 作为现有用户，我希望升级后无需改配置即可继续用微信，同时能按需启用飞书。

#### Acceptance Criteria

1. THE `config.Config` SHALL 新增 `Platforms map[string]PlatformConfig`（key 为 `"wechat"`/`"feishu"`），且为可选字段。
2. THE `PlatformConfig` SHALL 包含 `Enabled`、`AllowedUsers []string`、`DefaultAgent`、`Progress *ProgressConfig`。
3. WHEN `platforms` 缺省 THE SYSTEM SHALL 行为等同"仅 wechat 启用"：读取 `~/.weclaw/accounts/` 下所有微信账号，沿用现有全局 `Progress`/`DefaultAgent`。
4. THE 微信凭证 SHALL 继续存于 `~/.weclaw/accounts/`；THE 飞书凭证 SHALL 存于 `~/.weclaw/platforms/feishu.json`。
5. THE progress 配置覆盖优先级 SHALL 为 `platform.progress` > `agent.progress` > 全局 `progress` > 默认，复用现有 `NormalizeProgressConfig` 合并语义。
6. THE SYSTEM SHALL 保证现有 `config.json` 在升级后可正常解析（新增字段不破坏旧文件）。

_Validates: Property 9（微信零回归）_

---

## Requirement 10: 多平台启动与统一访问控制（阶段 2）

**User Story:** 作为用户，我希望一条 `weclaw start` 同时拉起所有已启用平台，并对所有平台强制访问控制。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `platform.Registry`，按 `config.platforms`（含 legacy `~/.weclaw/accounts/`）构建并启动所有启用平台。
2. WHEN `weclaw start` 运行 THE Registry SHALL 并发拉起微信与飞书 adapter，每个平台沿用带退避的重启策略（复用现有 `runMonitorWithRestart` 语义）。
3. THE Registry SHALL 通过 `guardedDispatch` 在分发给 `messaging` 之前对所有平台统一执行访问控制。
4. WHEN 入站消息的 `UserID` 不在该平台白名单 THE SYSTEM SHALL 静默丢弃并记录日志，且 SHALL NOT 触达 agent 执行路径。
5. WHEN 收到 SIGINT/SIGTERM THE SYSTEM SHALL 优雅停止所有平台（飞书关闭长连接，微信沿用现有逻辑）；daemon/PID 管理 SHALL 保持不变。
6. THE `api/server.go` 主动发消息 SHALL 改为通过 Registry 按 `(platform, accountID, chatID)` 定位 adapter 发送，并保持 `APIToken` 鉴权。

_Validates: Property 4（访问控制强制）_

---

## Requirement 11: 访问控制默认拒绝（阶段 2 / 安全）

**User Story:** 作为对安全敏感的用户，我希望未配置白名单时默认拒绝所有人，避免 bot 被任意联系人用来执行 shell。

#### Acceptance Criteria

1. WHEN 某平台 `allowed_users` 为空 THE SYSTEM SHALL 拒绝该平台的所有入站消息（fail-safe，拒绝所有）。
2. WHEN 某平台 `allowed_users` 为空 THE SYSTEM SHALL 在启动时打印醒目安全告警，引导用户配置 `allowed_users`。
3. WHEN `allowed_users` 非空 THE SYSTEM SHALL 仅允许名单内 `UserID` 触达 agent。
4. WHEN 访问被拒绝 THE SYSTEM SHALL 静默丢弃（不向发送者回复）以避免暴露 bot 存在性，仅记录日志。
5. THE 文档与启动日志 SHALL 明确告知用户"bot 可驱动具备 shell 权限的 AI agent"。

_Validates: Property 4（访问控制强制）_

---

## Requirement 12: 微信连接看门狗（阶段 4）

**User Story:** 作为微信用户，我希望电脑休眠/断网恢复后 bot 能自动重连，而不是静默卡死。

#### Acceptance Criteria

1. THE `wechat` adapter SHALL 运行看门狗 goroutine，每约 60s 检查一次最近成功响应时间。
2. WHEN 距上次成功 `getupdates` 响应超过约 5 分钟 THE SYSTEM SHALL 强制取消当前长轮询并触发重连。
3. THE 看门狗 SHALL 在 ctx 取消时退出。

_Validates: Property 9（微信零回归，行为为增强不破坏既有）_

---

## Requirement 13: 微信回显过滤（阶段 4 / 安全相关）

**User Story:** 作为用户，我希望 bot 不会把自己发出的消息当作用户输入再次处理。

#### Acceptance Criteria

1. THE 微信出站 `client_id` SHALL 使用 `weclaw:` 前缀（如 `weclaw:<uuid>`）。
2. WHEN 入站消息的 `client_id` 以 `weclaw:` 开头 THE adapter SHALL 跳过该消息（不触发 agent）。
3. THE SYSTEM SHALL 对带已知机器人提示前缀的回显文本做二次过滤（借助现有去重机制）。
4. THE `handler` 测试 SHALL 补充"自身回显不触发 agent"的用例。

_Validates: Property 7（去重幂等）_

---

## Requirement 14: 微信消息聚合（阶段 4）

**User Story:** 作为微信用户，我希望"图片+文字"分多条发出时被合并成一条交给 agent。

#### Acceptance Criteria

1. THE `wechat` adapter SHALL 在 per-user 串行队列出口对同一用户连续消息做时间窗聚合，默认窗口 800ms。
2. WHEN 聚合发生 THE SYSTEM SHALL 拼接文本并合并附件为一条 `IncomingMessage`。
3. IF 消息以命令前缀（`/`）开头 THEN THE SYSTEM SHALL 不将其纳入聚合（立即处理）。
4. THE 聚合窗口 SHALL 可配置；WHEN 配置为 0 THE SYSTEM SHALL 关闭聚合（逐条处理）。

_Validates: Property 7（去重幂等）_

---

## Requirement 15: 微信重连增强与 context_token 持久化（阶段 4）

**User Story:** 作为用户，我希望 session 过期时能更稳健地恢复，并且重启后仍能主动给微信用户发消息。

#### Acceptance Criteria

1. THE 微信重连 SHALL 采用 stepped backoff（约 `[3s,5s,10s,20s,30s]`）。
2. WHEN bot token 本身失效（sync buf 已空仍持续 `errcode -14`）THE SYSTEM SHALL 进入 fatal 慢探测模式（更长间隔，如 60s）并持续提示 `weclaw login`。
3. THE SYSTEM SHALL 将每个 `(platform, user)` 最近的 `context_token` 持久化到 `~/.weclaw/accounts/{botID}.tokens.json`，并在启动时加载。
4. WHEN weclaw 重启后收到主动发消息请求 THE SYSTEM SHALL 能使用持久化的 `context_token` 发送（无需等待用户先发言）。
5. THE 飞书平台 SHALL 用 `chat_id` 主动发消息（无 context_token 概念），对应字段留空。

_Validates: Property 9（微信零回归，增强不破坏）_

---

## Requirement 16: 软配置热重载（阶段 4）

**User Story:** 作为用户，我希望调整 default_agent / progress / allowed_users 后无需重启即可生效。

#### Acceptance Criteria

1. THE SYSTEM SHALL 对 `default_agent`、`progress`、`allowed_users` 等软配置采用带 mtime 缓存的按需重载（每条消息检查，文件未变时近零开销）。
2. THE SYSTEM SHALL NOT 热重载平台凭证或"是否启用某平台"（这些变更需重启以安全重建长连接生命周期）。
3. WHEN 配置文件解析失败 THE SYSTEM SHALL 回退到启动时快照并记录告警。

_Validates: Property 9（微信零回归）_

---

## Requirement 17: 测试与质量门槛（贯穿各阶段）

**User Story:** 作为维护者，我希望每个阶段都有清晰的测试覆盖与零回归保证。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `platform.Platform` / `Replier` / `Stream` 的 mock 实现，使 `HandleMessage` 测试与具体平台解耦。
2. THE SYSTEM SHALL 为飞书事件解析提供单测（text/post/image → IncomingMessage，open_id 抽取，HTML 清洗，post 展开，card.action → CardAction，权限错误码 → 引导）。
3. THE SYSTEM SHALL 为 CardKit 流式状态机提供单测（节流、sequence 单调递增、状态码分支）。
4. THE SYSTEM SHALL 为 choice 检测与会话 key 迁移提供单测。
5. THE 每个阶段 SHALL 以 `go build ./...`、`go vet ./...`、`go test ./...` 全绿为验收门槛。
6. THE 各阶段 SHALL 可独立回滚（飞书默认 `enabled:false`；流式 / 按钮 / 聚合各有独立开关）。

_Validates: Property 9（微信零回归）、Property 8（文本分段保真）_

---

## Correctness Properties 交叉引用汇总

| Property | 描述 | 关联需求 |
|----------|------|---------|
| Property 1 | 平台解耦不变量 | R1, R4 |
| Property 2 | 最终结果送达 | R5, R6, R8 |
| Property 3 | 能力降级安全 | R1, R8 |
| Property 4 | 访问控制强制 | R7, R10, R11 |
| Property 5 | 会话隔离 | R3 |
| Property 6 | 选择回流一致性 | R7 |
| Property 7 | 去重幂等 | R13, R14 |
| Property 8 | 文本分段保真 | R2, R17 |
| Property 9 | 微信零回归 | R2, R9, R12, R15, R16, R17 |
