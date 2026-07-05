# 当前任务记录

## 目标

支持从 WeClaw 查看 Claude Code 可选模型列表：新增 `/cc model status` 与 `/cc model ls`，只做查询展示，不切换模型，不启动 Claude Code 交互式界面。

## 执行任务

- [x] P1 串行：补 Claude 模型命令路由与渲染测试，先验证失败。
- [x] P2 串行：新增 Claude 模型查询接口与 CLI Agent 实现。
- [x] P3 串行：接入 `/cc model status|ls` 路由和帮助文案。
- [x] P4 串行：运行最小充分验证与交付前 review-gate。

## 并行评估

本轮不启用 subagent。改动集中在同一条 `/cc` 命令链路、同一组接口和测试，存在明显写冲突；串行执行更安全。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./agent -count=1 -timeout 60s
git diff --check
```

## Review 小结

已新增 Claude Code 模型查看能力：`/cc model status` 展示当前 Claude 模型配置，`/cc model ls` 展示 Claude Code 常用模型清单，并在输出中说明实际可用性仍受账号、组织策略和 provider 限制。本轮新增 `agent.ClaudeModelAgent`、`agent.DefaultClaudeModels`、CLI Agent 查询实现和 `messaging` 层渲染/路由；未实现模型切换，未启动 Claude Code 交互式界面，未读取密钥。

验证命令：`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -run 'TestClaudeModel' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./agent -run 'TestCLIAgent.*ClaudeModel' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./agent ./messaging`、`git diff --check`，结果均通过。`go test ./agent -count=1 -timeout 60s` 在当前沙箱因既有 Companion/HTTP 测试需要监听本地端口、以及既有 ACP 测试写入 `~/.weclaw/state` 被拒绝而失败，失败点与本轮新增代码无关；未继续扩大验证范围。
