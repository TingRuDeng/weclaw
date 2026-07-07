# 当前任务记录

## 目标

为飞书机器人配置增加用户友好的中文识别能力：保留 `name` 作为 ASCII 内部 ID 和凭证文件名，新增中文 `display_name` 与 `aliases`，让 CLI 查询类命令可以通过中文名或别名定位到同一个 bot。

## 已确认方案

- `name` 继续只允许 `a-zA-Z0-9._-`，用于 `~/.weclaw/platforms/feishu/<name>.json`，避免路径和脚本兼容风险。
- `display_name` 用于展示，可填写中文。
- `aliases` 用于识别，可填写多个中文或英文别名。
- CLI 接收 bot 参数时先按 `name` 精确匹配，再按 `display_name` 和 `aliases` 匹配；匹配不到或多重匹配时返回明确错误。

## 执行任务

- [x] P0 串行：补 RED 测试，覆盖配置字段、重复别名校验、`feishu add` 写入中文展示名/别名、CLI 按中文名解析。
- [x] P1 串行：实现 `FeishuBotConfig` 的 `display_name` / `aliases` 字段与校验逻辑。
- [x] P2 串行：实现 bot 引用解析 helper，并接入 `feishu login/status/bootstrap` 等凭证命令。
- [x] P3 串行：扩展 `weclaw feishu add` 交互与 flag，写入 `display_name` / `aliases`。
- [x] P4 串行：运行定向、包级、全量测试、vet 和 diff check。
- [x] P5 串行：执行 review-gate 交付前审查。

## 并行评估

本轮不启用 subagent。变更集中在 `config` 与 `cmd` 的共享结构和命令入口，同一轮并行写入会产生文件级冲突；串行 TDD 更清晰。只读分析已完成，无需网络检索。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./config -run 'TestValidateFeishuBot' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestRunFeishu(Add|Status|Login|Bootstrap)|TestResolveFeishuBot' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd ./config -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
git diff --check
```

## Review 小结

终态：finished。Spec 符合度：已保留飞书 bot `name` 作为 ASCII 内部 ID，并新增 `display_name` 与 `aliases`；`weclaw feishu add` 和 `bootstrap` 可写入展示名/别名，`login`、`status`、`bootstrap` 可通过中文展示名或别名解析到内部 ID。

安全检查：`app_secret` 仍只保存到凭证文件，不写入 `config.json`；新增别名解析不执行外部命令、不拼接 Shell、不引入 secret 或静默降级。`display_name`/`aliases` 在配置边界做去歧义校验，避免同一中文名匹配多个 bot。

测试与验证：RED 测试先因 `DisplayName`/`Aliases` 字段和 `resolveFeishuBotName` 缺失失败；实现后以下命令均通过：`GOCACHE=/private/tmp/weclaw-go-cache go test ./config -run 'TestValidateFeishuBotsRejectsDuplicateAlias' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestRunFeishuAddPromptsAndBootstrapsBot|TestRunFeishuStatusResolvesDisplayName|TestResolveFeishuBotNameMatchesAlias' -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd ./config -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`git diff --check`、`python3 scripts/validate_docs.py . --profile generic`。

复杂度检查：新增生产文件 `cmd/feishu_bootstrap.go`、`cmd/feishu_login_status.go`、`cmd/feishu_bot_ref.go`、`config/feishu_bot.go` 均低于 300 行；`cmd/feishu.go` 已从 309 行拆到 100 行。既有 `config/config.go` 仍超过 300 行，本轮只在现有结构体和校验入口做最小接入，未顺手扩大拆分范围。

Document-refresh: not-needed
原因：本轮未修改用户说明文档，配置新增字段可由 `weclaw feishu add` 交互写入；项目文档索引校验通过。

剩余风险：`display_name` 和 `aliases` 只解决 WeClaw CLI 本地识别，不会改变飞书开放平台里的机器人展示名。

潜在技术债：`config/config.go` 是既有大文件，后续若继续改配置结构，应单独拆分配置模型和平台校验逻辑。

结论：通过。
