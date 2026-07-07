# 删除命令兼容路由计划

## 目标

删除 WeClaw 中已经不需要保留的兼容命令路由，降低命令体系重复度，让代码、帮助文本、README 和测试只保留当前主入口。

## 非目标

- 不删除当前主入口。
- 不合并语义不同的命令，例如 `/status` 与 `/ps`、`/cancel` 与 `/stop`。
- 不改变 Agent 自定义别名能力。

## 当前事实

- `messaging/platform_commands.go` 的 `handleBuiltInPlatformCommand` 仍识别 `/info`、`/clear` 和 `/sw` 兼容入口。
- `messaging/command_detection.go` 的 `isCodexSessionCommandToken` 同时识别 `/cx` 和 `/codex` 作为 Codex 会话命令入口。
- `messaging/claude_session_handler.go` 的 `isClaudeSessionCommandToken` 同时识别 `/cc` 和 `/claude` 作为 Claude 会话命令入口。
- `messaging/codex_session_command.go` 中 `/cx open-app`、`/cx attach app` 与 `/cx app` 重复。
- `messaging/codex_model.go` 和 `messaging/claude_model.go` 中 `list` 与 `ls` 重复。
- `messaging/approval_mode.go` 中 `/mode ask`、`/mode off` 与 `/mode default` 重复。
- `cmd/update.go` 中 CLI `upgrade` 与 `update` 重复。

## 决策日志

- 主入口保留：`/status`、`/new`、`/update`、`/cx`、`/cc`、`/cx app`、`/cx model ls`、`/cc model ls`、`/mode default`。
- 删除兼容入口：`/info`、`/clear`、`/sw`、远程 `/upgrade`、CLI `upgrade`、Codex 会话 `/codex ...`、Claude 会话 `/claude ...`、`/cx open-app`、`/cx attach app`、模型子命令 `list`、`/mode ask`、`/mode off`。
- 保留发送消息入口：`/codex <内容>` 与 `/cc <内容>`，因为它们不是会话管理兼容路由，而是用户可见主发送入口。

## 执行计划

- [x] P0 串行：补 RED 测试，证明被删除的兼容入口不再被内置命令消费。
- [x] P1 串行：修改 `messaging/platform_commands.go`、`messaging/command_detection.go`、`messaging/claude_session_handler.go`、`messaging/codex_session_command.go`、`messaging/codex_model.go`、`messaging/claude_model.go`、`messaging/approval_mode.go`、`messaging/admin_commands.go`。
- [x] P2 串行：修改 CLI `cmd/update.go`，移除 `upgrade` 子命令注册。
- [x] P3 串行：同步 `messaging/help_text.go`、`README_CN.md`、`docs/AI_CONTEXT.md` 和相关测试期望。
- [x] P4 串行：运行定向测试、包级测试、全量测试、vet、文档校验和 diff check。
- [x] P5 串行：执行 review-gate。

## 验证矩阵

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'Test.*Command|Test.*Help|Test.*Session|Test.*Model|Test.*Mode|TestServiceAdmin' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'Test.*Update|Test.*Root|Test.*Command' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging ./cmd -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## 进度记录

- 规划完成，等待用户确认。
- P0 完成：新增 RED 测试，已验证当前兼容路由仍生效并导致测试失败。
- P1 完成：删除聊天内置命令兼容入口，新增 messaging 定向测试已通过。
- P2 完成：删除 CLI `upgrade` 子命令注册，cmd 定向测试已通过。
- P3 完成：同步帮助文本、README、AI_CONTEXT 和旧测试期望；旧命令残留仅保留在防回归测试和“不要重新引入”说明中。
- P4 完成：`go test ./...`、`go vet ./...`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check` 均通过。
- P5 完成：review-gate 结论通过；变更符合已确认计划，未发现新增 secret、静默 fallback 或未验证风险。

## Review 小结

- Spec 符合度：已删除计划列出的兼容路由，并保留 `/update`、`/cx ...`、`/cc ...`、`/cx app`、`model ls`、`/mode default` 等主入口。
- 安全检查：未新增外部输入执行路径、secret、权限绕过或静默降级。
- 测试覆盖：新增防回归测试覆盖被删除的聊天兼容入口、模型 `list` 别名、`/mode ask/off` 和 CLI `upgrade` 子命令。
- 文档刷新：已同步 README_CN 与 AI_CONTEXT；旧入口残留只用于测试断言和防重新引入说明。
- 剩余风险：`/codex <内容>` 仍作为发送给 Codex 的主入口保留，不能再用于会话管理命令。
