# 当前任务记录

## 目标

修复远程执行 `/restart` 后缺少完成反馈的问题：旧进程要明确告知“重启完成后会再通知”，新进程启动后要主动向原飞书或微信会话发送“重启完成”通知。

## 现状分析

- `messaging/admin_commands.go` 的 `handleServiceAdminCommand` 只在旧进程里发送开始通知，并由 `runServiceAdminCommand` 回写执行器结果。
- `messaging/admin_commands.go` 的 `scheduleRestartCommand` 会延迟启动新进程，但当前没有把原平台、账号、会话保存到跨进程状态。
- `cmd/start.go` 启动后会构建 `platform.Registry`，而 `platform/registry.go` 的 `ReplierFor` 已支持通过平台、账号、会话 ID 主动发送消息。

## 功能点

- `/restart --force` 执行器成功调度后，保存一条待完成通知，包含平台、账号、会话和操作者。
- 旧进程返回的 `/restart` 结果必须明确说明：服务恢复后会自动发送完成通知。
- 新进程启动后消费待完成通知，并通过 `platform.Registry.ReplierFor` 主动发送到原飞书或微信会话。

## 风险与决策

- 待通知记录只保存路由元数据，不保存密钥或消息正文。
- 启动完成通知按 best-effort 处理；若平台发送失败，记录日志，不让服务启动失败。
- 本轮不启用 subagent。改动集中在同一条重启链路和同一组测试文件，并行写入收益低且容易制造冲突。

## 执行任务

- [x] P0 串行：补 RED 测试，覆盖 `/restart --force` 后写入待完成通知。
- [x] P1 串行：补 RED 测试，覆盖启动后消费待完成通知并主动发送。
- [x] P2 串行：实现待完成通知的持久化、消费和发送逻辑。
- [x] P3 串行：接入 `/restart` 管理命令和 `cmd/start.go` 启动流程。
- [x] P4 串行：运行定向、包级、全量测试、vet 和 diff check。
- [x] P5 串行：执行 review-gate 交付前审查。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestServiceAdminRestart' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging ./cmd -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
git diff --check
```

## Review 小结

终态：finished。Spec 符合度：已实现旧进程 `/restart` 成功调度后保存飞书或微信会话路由，新进程启动到可主动发送阶段后回写“重启完成”通知；旧进程返回文案已明确说明完成后会自动通知。

安全检查：待通知记录只包含平台、账号、会话、用户和时间，不包含 app_secret、token、消息正文或其它凭据；路径固定在用户主目录 `.weclaw/state` 下，不拼接外部输入作为文件路径。

测试与验证：新增测试覆盖待通知记录写入、启动后主动发送完成通知、发送目标缺失时保留记录重试。验证通过：`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestServiceAdminRestart|TestDeliverPendingRestart|TestFormatServiceAdminCommandReply' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging ./cmd -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`git diff --check`、`python3 scripts/validate_docs.py . --profile generic`。

复杂度检查：新增生产文件均低于 300 行；`messaging/admin_commands.go` 已拆出回复格式化逻辑，当前 215 行；单函数均保持短小，无新增深层嵌套。

Document-refresh: not-needed
原因：本轮是远程重启反馈行为修复，不改变用户配置格式、命令语法或文档索引。

剩余风险：完成通知依赖平台主动发送能力；如果飞书或微信发送失败，记录会保留到下次启动重试，当前不会在运行中周期重试。

潜在技术债：管理命令的跨进程状态目前是文件级 JSON；若后续管理命令增多，可统一抽成管理命令状态存储。

结论：通过。
