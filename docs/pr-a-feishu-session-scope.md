# PR-A 飞书会话范围收口记录

## 1. 背景

PR-A 解决飞书端会话路由隐式、不稳定的问题，重点收口 DM、群聊、话题 thread、群聊 @ 触发、重复事件和“同时发送到群”镜像消息的行为。

目标是让飞书端 session 不串线程，群聊默认安全不乱触发，所有会话路由都可解释。

## 2. 最终会话模型

- `UserID` 始终保持真实发送者 `open_id`。
- `routeUserID` / `feishu_session_key` 只用于 agent session routing。
- allowed_users、审计、日志、workspace 绑定仍按真实 `UserID` 判断。
- 飞书 session routing 不覆盖真实发送者身份。

## 3. Feishu session key 规则

- DM：`chat_id + sender_open_id`
- group no thread：`chat_id`
- group thread：`chat_id + thread_key`
- `thread_key` 优先级：`root_id > thread_id > message_id`

## 4. 群聊触发规则

- `require_mention_in_group` 默认 `true`
  - 群聊默认必须 @bot 才触发 agent。
- `thread_isolation` 默认 `true`
  - 飞书话题 / thread 默认独立 session。
- DM 不受群聊 @ 规则影响。

## 5. “同时发送到群”的处理规则

飞书话题里勾选“同时发送到群”时，飞书会产生两条输入：

1. thread 内消息
2. 群主会话镜像消息

当前实现规则：

- 优先保留 thread 消息。
- 忽略无 thread 的群主会话镜像消息。
- mirror dedup 不改变 session key 语义。
- 不影响普通群聊消息。
- 不影响 DM。
- 不影响 UserID / routeUserID 分离。

## 6. 验收结果

- DM 私聊：通过
- 群聊未 @bot 默认不触发：通过
- 群聊 @bot 正常响应：通过
- 不勾选“同时发送到群”时，不同 thread 不串上下文：通过
- 勾选“同时发送到群”时，只出现一个卡片：通过
- 勾选“同时发送到群”时，日志只 dispatch 一次：通过
- 勾选“同时发送到群”时，优先进入 thread session：通过
- UserID / routeUserID 分离：通过
- 飞书重复事件短期去重：通过

## 7. 后续 PR 不应破坏的约束

- 不得用 `feishu_session_key` 覆盖真实 `msg.UserID`。
- allowed_users、workspace、审计、日志必须继续基于真实发送者 `open_id`。
- 群聊默认必须 @bot 才触发，除非显式关闭 `require_mention_in_group`。
- thread isolation 默认开启，除非显式关闭 `thread_isolation`。
- thread session key 必须保持 `root_id > thread_id > message_id` 的优先级。
- “同时发送到群”的镜像群消息不得导致重复 dispatch。
- 飞书修复不得影响微信逻辑。
