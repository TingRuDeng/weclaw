# 飞书回复串多会话体验

## 目标

- 让飞书群聊里的回复串 / 话题成为 WeClaw 的一等会话边界。
- 同一飞书群内不同回复串可以并行承载不同 Codex 会话，减少在一个窗口内来回切换的心智负担。
- 飞书回复、卡片、进度和任务结果尽量回到触发任务的原回复串 / 话题。

## 非目标

- 不让机器人为同一用户创建多个飞书单聊窗口。
- 不默认自动建群、拉人或改变飞书群成员关系。
- 不改变微信侧会话模型。
- 不重做 Codex thread 存储模型，只补齐飞书 thread 维度和出站路由。

## 当前事实

- `feishu/session_scope.go` 的 `BuildFeishuSessionKey` 已支持按群聊 `root_id/thread_id` 生成 `feishu_session_key`。
- `messaging/platform_message.go` 的 `platformMessageRouteUserID` 已把飞书 `feishu_session_key` 作为业务路由键。
- `feishu/adapter_events.go` 的 `dispatchIncomingMessage` 当前只用 `chat_id/open_id` 创建 `Replier`，未把 `ReplyToID` 或 thread 信息传给出站发送。
- `feishu/replier.go` 和 `feishu/sender.go` 当前通过 `im.message.create` 向 `chat_id/open_id` 发送消息，未显式回复原消息。

## 决策日志

- 采用“飞书回复串 / 话题 = WeClaw route session”的主方案。
- 先补出站 thread-aware reply，再优化卡片提示和测试，避免只做 UI 层假多会话。
- 仍保留 `/cx ls` 作为单聊和手动切换入口。

## 执行计划

- [x] P1 串行：确认飞书 SDK 是否支持 `im.message.reply` 或等价回复接口，并封装到 sender。
- [x] P2 串行：扩展飞书 Replier，让文本、卡片、图片、文件和 CardKit 流式卡片可回到原消息 / 原回复串。
- [x] P3 串行：让 `dispatchIncomingMessage` 用入站 `ReplyToID/ContextToken` 创建 thread-aware Replier。
- [x] P4 串行：补飞书入站、出站和卡片回调测试，覆盖不同回复串隔离、按钮保持 session key、结果回原串。
- [x] P5 串行：执行最小充分验证和 review-gate。

## 进度记录

- P1：已确认本项目使用的 `github.com/larksuite/oapi-sdk-go/v3@v3.9.7` 提供 `client.Im.V1.Message.Reply`，请求体支持 `reply_in_thread`，可用于把回复发回原消息 / 话题。
- P2：已为飞书 sender/replier 增加文本、图片、文件、卡片和 CardKit 流式卡片的原消息回复路径；审批卡仍按 owner 私发，不回群聊串。
- P3：`dispatchIncomingMessage` 已用入站 `ReplyToID/ContextToken` 创建 thread-aware Replier，普通业务回复会回到触发消息所在回复串 / 话题。
- P4：已补测试覆盖不同 `root_id` 生成不同 `feishu_session_key`、入站普通回复回原消息、卡片点击后的业务回复回卡片消息、普通卡片回原串、审批卡仍发 owner。

## 验证结果

- `go test ./feishu -run 'TestReplierSendTextRepliesToSourceMessage|TestReplierSendMediaRepliesToSourceMessage|TestReplierAskChoicesRepliesCardToSourceMessage|TestReplierOpenStreamRepliesCardToSourceMessage|TestReplierApprovalCardStillSendsToOwner|TestSDKMessageSenderRepliesInThread' -count=1 -timeout 60s`：通过。
- `go test ./feishu -run 'TestToIncomingFromMessageSeparatesGroupReplyThreads|TestHandleCardActionEventReplyUsesCardMessage' -count=1 -timeout 60s`：通过。
- `go test ./feishu -count=1 -timeout 60s`：通过。
- `go test ./... -count=1 -timeout 120s`：通过。
- `go vet ./...`：通过。
- `python3 scripts/validate_docs.py . --profile generic`：通过。
- `git diff --check`：通过。

## Review 小结

- 终态：finished。
- Spec 符合度：通过；实现聚焦“飞书回复串 / 话题 = WeClaw 会话边界”，未引入自动建群或多单聊窗口。
- 安全检查：通过；未新增 secret，审批卡仍按 owner 私发，未回群聊串。
- 测试与验证：通过；已覆盖 sender、replier、adapter 入站和卡片回调路径。
- 复杂度检查：通过；`feishu/sender.go` 拆出 `feishu/sender_reply.go` 后保持单文件 300 行以内。
- Document-refresh: not-needed。原因：本次是飞书平台行为实现和任务记录，不改变公开配置或文档契约。
- 剩余风险：飞书回复接口权限不足时会暴露 API 错误，需要实际线上权限验证。
- 潜在技术债：单聊仍是单窗口卡片式多会话，不属于本轮目标。
- 结论：通过。

## 回归修复记录

- 2026-07-06：发布后发现飞书单聊 DM 也被 `message.reply` 回复，客户端表现为每条消息都自动开回复串。根因是 `dispatchIncomingMessage` 对所有飞书消息都启用了 thread-aware Replier。
- 修复策略：仅群聊 / 话题群或 `feishu_session_key` 为 group 时启用回复串；DM 继续普通发送到单聊窗口。
- 回归测试：`TestHandleMessageEventDMReplyUsesFreshMessage`、`TestHandleCardActionEventDMReplyUsesFreshMessage`。
- 2026-07-06：新增飞书单聊 `/cx new-thread` 后发现一个 DM 子会话切换工作空间，其他子会话也跟随切换。根因是 route 级 `/cx cd` 和 `/cx switch` 仍写入 Codex Agent 全局 cwd，未预置 active workspace 的子会话会从全局 cwd 兜底。
- 修复策略：`dm_thread` route 独立保存 active workspace；飞书子会话解析工作空间时只返回 route 路径，不改真实用户 owner workspace，也不改 Agent 全局 cwd。`/cx app`、`/cx cli`、`/new` 同步按 route 当前 workspace 执行。
- 回归测试：`TestFeishuDMThreadWorkspaceSwitchDoesNotAffectOtherThreads`、`TestFeishuDMThreadWorkspaceSwitchDoesNotMutateDefaultWorkspace`。
