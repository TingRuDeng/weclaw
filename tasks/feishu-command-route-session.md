# 飞书命令路由会话修复

## 目标

- 飞书群聊和 thread 中的普通消息、`/new`、`/cx`、`/stop`、`/run`、`/guide`、`/cancel` 操作同一个当前会话。
- 权限、审计、日志、审批归属继续使用真实发送者 `UserID`。
- 飞书按钮卡片回调保留原始 `feishu_session_key`。

## 非目标

- 不修改 PR-A session key 规则。
- 不修改 allowed_users 语义。
- 不修改审批卡片状态体验。
- 不修改微信会话路由。
- 不做工作空间锁定或权限档位调整。

## 当前事实

- 普通飞书消息通过 `platformMessageRouteUserID` 使用 `feishu_session_key` 路由。
- 内置命令当前主要使用 `msg.UserID`，导致群聊/thread 命令和普通消息操作的会话不一致。
- 飞书按钮卡片回调当前未携带原消息的 `feishu_session_key`。

## 决策日志

- 保留 `actorUserID = msg.UserID` 用于真实操作者语义。
- 新增或传递 `routeUserID = platformMessageRouteUserID(msg)` 用于当前会话控制。
- `/cwd` 暂不切到 `routeUserID`，避免改变用户工作空间绑定语义。

## 执行计划

- [x] 串行：确认分支、工作树和相关 lessons。
- [x] 串行：补充失败测试，覆盖飞书文本命令与按钮命令的 route key。
- [x] 串行：调整 handler 内置命令使用 actor/route 分离。
- [x] 串行：飞书按钮卡片透传 session key。
- [x] 串行：运行最小充分验证和 review-gate。

## 验证计划

- `go test ./feishu ./messaging`
- `go test ./...`
- `git diff --check`

## Review 小结

- `go test ./feishu ./messaging` 通过。
- `go test ./... -timeout 60s` 通过。
- `git diff --check` 通过。
