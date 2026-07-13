# 当前任务记录

## 目标

修复飞书或微信显式切换 Claude/Codex 会话后，下一条普通消息仍发送给原 Agent 的问题。

## 根因

Agent 专属 session store 与窗口级 `agentSessionStore` 是两套状态。`/cc switch` 等命令只更新前者，普通消息仍按窗口旧绑定或账号默认 Agent 路由。

## 任务清单

- [x] TDD：复现切换 Claude session 后普通消息仍进入 Codex。
- [x] 实现：Claude switch/new 成功后同步窗口 Agent。
- [x] TDD：覆盖 Codex switch/new 的反向切换。
- [x] 实现：Codex switch/new 成功后同步窗口 Agent。
- [x] 边界：目标 session 操作失败时保留原窗口 Agent。
- [x] 验证：消息层测试、竞态检测、全仓测试、vet、staticcheck 和文档检查。
- [x] 审查：检查权限、持久化顺序、复杂度和剩余风险。

## 验证结果

`go test ./messaging -count=1 -timeout 60s`、`go test -race ./messaging -count=1 -timeout 120s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、`staticcheck ./...`、文档校验和 `git diff --check` 均通过。

## Review 小结

修复只改变成功的显式 Agent 会话操作；浏览工作空间不会切换窗口 Agent，失败恢复不会覆盖原绑定。源码尚未提交发布，本机运行中的 v0.1.165 仍保持旧行为。
