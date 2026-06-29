# 飞书 Codex 空间与会话卡片切换

## 目标

- 飞书里 `/cx ls` 返回可点击的 Codex 工作空间卡片。
- 点击工作空间后返回当前工作空间下的会话卡片。
- 点击会话后复用现有 `/cx switch` 逻辑完成切换。
- 微信端继续使用 `/cx ls`、`/cx cd`、`/cx switch` 命令文本，不改变现有行为。

## 非目标

- 不新增分页、搜索、收藏工作空间。
- 不重写 Codex workspace/thread 解析逻辑。
- 不改变微信命令交互。

## 执行计划

- [x] P1 串行：补飞书 `/cx ls` 工作空间卡片红灯测试。
- [x] P1 串行：补飞书工作空间按钮进入会话卡片红灯测试。
- [x] P1 串行：补 pending approval 不误消费 `/cx` 卡片选择红灯测试。
- [x] P1 串行：补飞书无效工作空间不被卡片覆盖的红灯测试。
- [x] P2 串行：实现飞书 Codex 导航卡片渲染与命令回放。
- [x] P2 串行：收紧 pending approval 只消费合法审批选项。
- [x] P2 串行：错误回复优先文本返回，不用卡片覆盖错误。
- [x] P3 串行：执行定向测试、回归测试、`go vet`、`git diff --check`。

## 验证记录

- 红灯：`go test ./messaging -run 'FeishuCodex|PendingApprovalIgnores' -count=1 -timeout 60s`，失败点覆盖飞书工作空间卡片、会话卡片、审批误消费。
- 绿灯：`go test ./messaging -run 'FeishuCodex|PendingApprovalIgnores' -count=1 -timeout 60s`，通过。
- 红灯：`go test ./messaging -run 'TestFeishuCodexInvalidWorkspaceReturnsTextError' -count=1 -timeout 60s`，失败点覆盖错误被卡片覆盖。
- 绿灯：`go test ./messaging -run 'TestFeishuCodexInvalidWorkspaceReturnsTextError' -count=1 -timeout 60s`，通过。
- 定向回归：`go test ./messaging ./feishu -run 'CodexCx|FeishuCodex|PendingApproval|Choice|RawCommand' -count=1 -timeout 60s`，通过。
- 核心回归：`go test ./agent ./messaging ./feishu ./cmd -count=1 -timeout 60s`，通过。
- 静态检查：`go vet ./...`，通过。
- 格式检查：`git diff --check`，通过。
- 发布阻断：`scripts/release.sh --next-patch` 在 `go test -race ./agent ./cmd ./messaging` 阶段发现测试 race；原因是审批测试并发读写 `platformtest.Replier.Choices`。
- 修复：审批测试改为等待 `pendingApprovals` 注册，不再并发读取 fake replier 的 slice。
- 阻断修复验证：`go test -race ./messaging -run 'ApprovalHandlerWaits|PendingApprovalIgnores' -count=1 -timeout 60s`，通过。
- Race 回归：`go test -race ./agent ./cmd ./messaging -count=1 -timeout 60s`，通过。

## Review 小结

- 终态：finished。
- Spec 符合度：通过；飞书 `/cx ls` 和工作空间点击返回按钮卡片，微信命令路径未改。
- 安全检查：通过；按钮 payload 仍走服务端现有 `/cx` 解析和 workspace/thread 校验，pending approval 增加合法选项白名单。
- 测试与验证：通过；覆盖卡片工作空间、卡片会话、审批误消费、错误文本优先。
- Document-refresh: not-needed；这是平台交互细节变更，任务记录已覆盖实现与验证。
- 剩余风险：未实现分页，工作空间或会话过多时卡片会较长。
- 潜在技术债：`AskChoices` 仍是通用 choice action，靠业务层合法选项和 `/cx` 命令分流隔离。
- 结论：通过。
