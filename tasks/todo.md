# 当前任务记录

## 目标

新增命令行交互式添加飞书机器人入口：`weclaw feishu add`。该命令应引导用户输入 bot 名称、飞书 `app_id`、`app_secret`、白名单、默认 Agent、进度模式和群聊 @ 规则，然后复用现有 bootstrap 落盘能力保存凭证并更新 `~/.weclaw/config.json`。

## 执行任务

- [x] P0 串行：等待用户确认本计划，未确认前不改业务代码。
- [x] P1 串行：先补 RED 测试，覆盖 `runFeishuAdd` 会按交互输入调用现有 bootstrap 流程，且输出不泄露 `app_secret`。
- [x] P2 串行：实现交互输入抽象和 `weclaw feishu add` 子命令，只提示缺失字段，secret 使用隐藏输入能力。
- [x] P3 串行：复用 `runFeishuBootstrap` 完成凭证校验、凭证保存、`platforms.feishu.bots[]` 更新和结果输出。
- [x] P4 串行：运行最小验证和全量验证。
- [x] P5 串行：执行 review-gate 交付前审查。

## 并行评估

本轮不启用 subagent。现有 `cmd/feishu.go` 与 `cmd/feishu_test.go` 已接近单文件行数上限，新增逻辑拆到 `cmd/feishu_add.go` 与 `cmd/feishu_add_test.go`；命令注册只在 `cmd/feishu.go` 做最小改动。同一 CLI 命令链路存在写冲突，串行 TDD 更清晰。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestRunFeishuAdd' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
git diff --check
```

## Review 小结

终态：finished。Spec 符合度：已实现 `weclaw feishu add` 交互式添加飞书机器人，交互收集 bot 名称、`app_id`、`app_secret`、白名单、默认 Agent、进度模式和群聊 @ 规则；最终复用 `runFeishuBootstrap` 做凭证校验、凭证保存和 `platforms.feishu.bots[]` 更新。

安全检查：`app_secret` 不写入 `config.json`，真实 TTY 使用隐藏输入；测试覆盖 stdout 不包含 secret。未引入硬编码 secret、Shell 拼接、无依据 fallback 或静默降级。

测试与验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestRunFeishuAdd' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`git diff --check` 均通过。`cmd` 包和全量测试因现有 `httptest` 需要监听本地端口，使用提升权限执行。

复杂度检查：新增逻辑拆到 `cmd/feishu_add.go` 和 `cmd/feishu_add_test.go`，相关文件均小于 300 行；新增 helper 参数数不超过 3，核心函数保持短小。

Document-refresh: not-needed
原因：本轮只新增 CLI 交互入口，现有 `bootstrap` 文档仍可用；未改变配置结构、运行时语义或发布流程。

剩余风险：交互式 add 会在未显式传 `--progress` 时默认写入 `stream`；这符合当前飞书体验目标，但与 bootstrap 的空值不覆盖语义不同。

潜在技术债：`feishu.go` 仍复用一组全局 flag 变量绑定多个子命令，这是既有模式，本轮只做最小接入未重构。

结论：通过。
