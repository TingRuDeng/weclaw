# 当前任务记录

## 目标

让飞书 `/cc ls` 使用与 Codex 一致的两级交互卡片：先选择 Claude 工作空间，再选择会话；微信保持文本列表。

## 已确认行为

- 工作空间卡片按钮进入工作空间，会话卡片按钮使用稳定 `sessionId` 切换。
- 进入空工作空间时只提示发送 `/cc new`，不得自动创建会话。
- 点击后仅收纳被点击的交互卡片。
- 切换成功后展示当前会话模型和推理强度。
- Claude 任务运行期间不插入导航卡片。
- 普通用户受 `allowed_workspace_roots` 限制，管理员保持豁免。

## 任务清单

- [x] P1 串行 TDD：补充 Claude 飞书工作空间卡片失败测试。
- [x] P2 串行 TDD：补充工作空间进入、会话卡片和空工作空间失败测试。
- [x] P3 串行实现：增加 Claude 工作空间分组与 `/cc cd` 导航。
- [x] P4 串行实现：接入飞书 Claude 会话卡片路由和运行态拦截。
- [x] P5 串行验证：执行定向测试、全仓测试、vet、staticcheck 和差异检查。
- [x] P6 串行审查：执行 review gate 并记录剩余风险。

## 并行说明

不使用 subagent。命令路由、活动工作空间和卡片回放共享同一状态链，串行实现可以避免状态写冲突。

## Review 小结

2026-07-12 完成 Claude 飞书两级会话卡片：`/cc ls` 展示权限过滤后的工作空间，`/cc cd` 进入工作空间并展示稳定 `sessionId` 会话按钮；空工作空间只提示 `/cc new`，运行中任务不插入导航卡片，微信继续使用原扁平文本列表。切换反馈复用现有会话状态读取，展示模型和推理强度。

自动验收通过：Claude 定向测试、`go test ./messaging ./feishu -count=1 -timeout 60s`、`go test -race ./messaging ./feishu -count=1 -timeout 120s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、`staticcheck ./...`、文档校验、格式化和 `git diff --check` 均为退出码 0。Review gate 未发现阻塞性权限、路由、状态一致性或复杂度问题；剩余风险是尚未在真实飞书 CardKit 环境完成按钮点击复测。
