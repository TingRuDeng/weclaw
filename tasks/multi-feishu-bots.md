# 单实例多飞书机器人支持

## 目标

- 支持一个 WeClaw 进程同时启动多个飞书机器人。
- 每个飞书机器人拥有独立的凭证、白名单、默认 Agent、进度配置和群聊 @ 触发配置。
- 继续遵守“一个飞书机器人 = 一个项目入口”的会话模型，避免恢复飞书回复串 / 话题子会话。

## 非目标

- 不恢复 `/cx new-thread` 或飞书回复串子会话。
- 不把 `app_secret` 写入普通 `config.json`。
- 不在本轮实现飞书菜单动态生成。
- 不支持旧单机器人配置的兼容迁移；当前按用户要求不保留旧配置兼容。

## 当前事实

- `cmd/start_platform.go:buildPlatformRegistry` 只读取 `platforms.feishu` 并调用一次 `feishu.LoadCredentials()`，因此只会创建一个飞书 Adapter。
- `feishu/config.go:CredentialsPath` 固定返回 `~/.weclaw/platforms/feishu.json`，没有按机器人名称或 `app_id` 隔离凭证。
- `cmd/feishu.go:runFeishuLogin` 与 `runFeishuStatus` 只处理一组飞书凭证。
- `platform/registry.go:ReplierFor` 已支持按 `AccountID` 选择平台实例，`feishu/adapter.go:AccountID` 返回飞书 `AppID`，底层已有多实例基础。
- `cmd/start_config_reload.go:applySoftConfig` 目前按平台名热更新白名单，不能区分同平台多个机器人。

## 决策日志

- 采用显式多 bot 配置：`platforms.feishu.bots[]`。
- 每个 bot 必须有稳定 `name`，用于配置、凭证文件和日志展示。
- 凭证按 bot name 保存到 `~/.weclaw/platforms/feishu/<name>.json`，避免 secret 进入普通配置。
- `app_id` 只允许出现在普通配置中，用于启动前选择正确凭证与展示；`app_secret` 只存在凭证文件或命令输入。
- 不兼容旧 `platforms.feishu.allowed_users` 这类单 bot 配置；旧用户需要改成 `bots[]`。

## 方案对比

### 方案 A：继续单实例单机器人

- 优点：无需改动。
- 缺点：与“一个机器人 = 一个项目入口”的产品方向冲突，多项目必须开多个 WeClaw 进程。
- 结论：淘汰。

### 方案 B：单实例多机器人，配置写 `bots[]`，凭证按 bot name 分文件

- 优点：入口清晰，secret 不进 `config.json`，启动层可直接循环创建 Adapter。
- 缺点：需要改配置、CLI、doctor、热重载和 Web 状态。
- 结论：推荐。

### 方案 C：用多个 `platforms.feishu_xxx` 伪平台

- 优点：表面上改动较少。
- 缺点：会污染平台抽象，`PlatformName`、命令路由、进度配置和文档都会变复杂。
- 结论：淘汰。

## 执行计划

- [x] P0 串行：补测试先失败
  - `config/config_test.go`：验证 `PlatformConfig.Bots` 的 JSON 解析与必填校验。
  - `feishu/config_test.go`：验证按 bot name 保存、读取凭证，拒绝非法名称。
  - `cmd/start_test.go`：验证两个飞书 bot 会创建两个 registry entry。
  - `platform/registry_test.go`：验证同平台多账号白名单热更新不会互相覆盖。
- [x] P1 串行：扩展配置模型
  - `config/config.go`：新增 `FeishuBotConfig`，挂到 `PlatformConfig.Bots`。
  - 增加 bot name 校验、`app_id` 校验和有效 bot 解析 helper。
- [x] P2 串行：扩展飞书凭证管理
  - `feishu/config.go`：新增 `CredentialsPathForBot`、`SaveCredentialsForBot`、`LoadCredentialsForBot`、`LoadCredentialsWithSourceForBot`。
  - 保持凭证文件 `0600`，目录 `0700`。
- [x] P3 串行：扩展 CLI
  - `cmd/feishu.go`：`login/status` 增加 `--name`，命令输出包含 bot name 与 app_id。
- [x] P4 串行：启动多个飞书 Adapter
  - `cmd/start_platform.go:buildPlatformRegistry`：循环 `platforms.feishu.bots[]` 创建 Adapter。
  - 每个 bot 使用自己的 `AllowedUsers`、`RequireMentionInGroup`。
- [x] P5 串行：热重载与状态检查
  - `platform/registry.go`：增加按平台 + accountID 更新访问控制的方法。
  - `cmd/start_config_reload.go:applySoftConfig`：按 bot app_id 更新白名单。
  - `cmd/doctor.go`：逐个 bot 检查凭证和白名单。
  - `web/status.go`、`web/handlers.go`：状态与凭证写入支持 bot name。
- [x] P6 串行：文档与示例
  - `README_CN.md`、`README.md`、`docs/AI_CONTEXT.md`：更新多飞书机器人配置示例和旧单 bot 配置废弃说明。
- [x] P7 串行：统一验证与 review
  - 跑定向测试、全量测试、vet、文档校验和 diff 检查。

## 验证矩阵

- `GOCACHE=/private/tmp/weclaw-go-cache go test ./config ./feishu ./platform ./cmd ./web -count=1 -timeout 60s`
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`
- `GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`
- `PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`
- `python3 scripts/validate_docs.py . --profile generic`
- `git diff --check`

## Review 小结

- 终态：finished。
- Spec 符合度：通过。已支持单实例多个飞书机器人，配置入口为 `platforms.feishu.bots[]`，凭证按 bot name 分文件保存，启动层按 bot 创建多个飞书 Adapter，热重载和 Web/doctor 状态按 `app_id` 隔离。
- 安全检查：通过。`app_secret` 仍只写入 `~/.weclaw/platforms/feishu/<name>.json`，普通 `config.json` 只保存 `app_id`；Web/API 和 CLI 不回显 secret。
- 测试与验证：通过。已执行受影响包测试、全量测试、`go vet ./...`、文档 validator、`git diff --check`。
- 复杂度检查：通过。本轮新增 helper 按配置、凭证、启动、热重载、状态职责拆分，未恢复飞书回复串子会话。
- 剩余风险：旧单 bot 配置会 fail-fast，需要用户迁移到 `platforms.feishu.bots[]` 并用 `weclaw feishu login --name <bot>` 重新保存凭证。
