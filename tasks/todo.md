# 当前任务记录

## 目标

切换到 Codex App 独立进程正在执行的会话时，不主动提示“需在 Codex App 中操作”；仅在用户实际发送 `/stop` 时说明跨进程停止限制。

## 执行任务

- [x] P1 串行：补充切换提示、任务列表和 `/stop` 文案回归测试，并确认旧实现失败。
- [x] P2 串行：隐藏主动操作提示，仅保留结果自动回传说明和按需 `/stop` 限制反馈。
- [x] P3 串行：执行 messaging、race、全仓测试、静态检查和 review gate。

## 规划

用户已确认方案：不使用杀进程或停止 watcher 冒充定向停止；共享 daemon 未接入前，仅对实际 `/stop` 明确跨进程限制。

## 并行说明

本轮不使用 subagent。改动集中在同一任务展示与控制状态，串行修改可避免文案和能力标记不一致。

## 进度记录

- 2026-07-11：官方源码确认 `turn/interrupt` 依赖当前 app-server 的 `ThreadManager` 和 active turn，独立 Codex App turn 不能由 WeClaw app-server 直接中断；本机未运行共享 daemon/control socket。
- 2026-07-11：P1 完成；旧实现的切换回复和 `/ps` 均包含主动 App 操作提示，测试按预期失败。
- 2026-07-11：P2 完成；切换回复和 `/ps` 只说明结果自动回传，实际 `/stop` 才返回跨进程限制；WeClaw 持有任务的 `/stop` 回归通过。
- 2026-07-11：P3 完成；messaging race、全仓测试、vet、文档契约和差异检查均通过。

## Review 小结

终态：finished。

Spec 符合度：通过。切换反馈和 `/ps` 不再主动提示回到 Codex App；实际 `/stop` 才说明独立 App turn 暂不支持从飞书或微信停止。

安全检查：未改变任务控制权限，未杀进程、未停止 watcher 冒充任务停止，也未把独立 App turn 错标为可控制；WeClaw app-server 持有的 turn 继续使用 `turn/interrupt`。

测试与验证：TDD 回归先失败后通过；`go test -race ./messaging -count=1 -timeout 60s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、文档契约和 `git diff --check` 均通过。

复杂度检查：生产代码仅修改三个文案分支，未新增函数或状态；相关生产文件均少于 300 行。

Document-refresh: not-needed

原因：公开命令和配置未变化，能力边界已写入任务记录与经验规则，用户可见提示由测试锁定。

剩余风险：若未来 Codex App 改为与 WeClaw 共享 app-server daemon，需要重新探测 control socket，并将该任务来源升级为可控制。

潜在技术债：Codex 共享 daemon 和 remote-control 仍是实验能力，当前未接入 WeClaw。

结论：通过。
