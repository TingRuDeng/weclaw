# 交互式添加飞书机器人计划

## 目标

实现 `weclaw feishu add`，用交互式问题完成飞书机器人首次配置，降低用户记忆 `bootstrap` 长参数的成本。

## 非目标

- 不改变飞书运行时长连接、事件订阅或权限模型。
- 不把 `app_secret` 写入 `~/.weclaw/config.json`。
- 不删除或迁移已有 `weclaw feishu bootstrap/login/status`。
- 不在本轮实现飞书开放平台自动开权限或自动发布版本。

## 当前事实

- `cmd/feishu.go` 已有 `weclaw feishu bootstrap`，`runFeishuBootstrap` 会校验凭证、调用 `saveFeishuCreds` 保存 secret、加载配置、调用 `upsertFeishuBotConfig` 更新 `platforms.feishu.bots[]`，最后 `config.Save`。
- `feishu/config.go` 已将 bot 凭证保存到 `~/.weclaw/platforms/feishu/<bot>.json`，权限为 `0600`；`config/config.go` 明确 `FeishuBotConfig` 不包含 secret。
- `config.Validate` 会拒绝飞书 legacy 单 bot 字段、`enabled=true` 但无 `bots[]`、重复 bot 名称和重复 `app_id`。
- `README_CN.md` 当前主要引导用户使用 `weclaw feishu bootstrap ...`。

## 决策日志

- 交互式 `add` 应复用现有 bootstrap 行为，避免形成第二套写配置路径。
- `app_secret` 必须通过隐藏输入读取；在非 TTY 或测试场景下可通过注入输入源读取，但输出仍不得回显 secret。
- 交互问题应只收集配置需要的最小字段；飞书权限检查和事件订阅仍通过后续提示或 `lark-cli` 完成。

## 方案对比

### 方案 A：新增 `feishu add`，作为交互式 bootstrap 包装

- 做法：`add` 读取缺失字段，组装 `feishuBootstrapOptions`，调用 `runFeishuBootstrap`。
- 优点：复用现有校验、保存和输出；影响范围小；不会出现两套配置语义。
- 缺点：需要新增可测试的交互输入抽象。

### 方案 B：让 `feishu bootstrap` 在缺少参数时自动交互

- 做法：不新增命令，增强 bootstrap 参数缺失时的行为。
- 优点：少一个命令。
- 缺点：破坏当前脚本化命令的 fail-fast 行为；CI/自动化中缺参数可能卡住。

### 方案 C：新增独立配置写入逻辑

- 做法：`add` 自己保存凭证和 config。
- 优点：实现直观。
- 缺点：重复 `runFeishuBootstrap` 逻辑，容易在 secret、默认值和 merge 规则上分叉。

## 推荐方案

采用方案 A。`weclaw feishu add` 是面向人类的交互入口；`weclaw feishu bootstrap` 继续作为脚本化入口。

## 执行计划

1. 在 `cmd/feishu_add_test.go` 增加 RED 测试：模拟交互输入，验证 `runFeishuAdd` 保存凭证、更新 bot 配置、开启飞书平台，且 stdout 不包含 secret。
2. 在 `cmd/feishu.go` 增加 `feishuAddCmd` 注册和 flags，核心实现放到 `cmd/feishu_add.go`，复用 `--name`、`--app-id`、`--app-secret`、`--allowed-users`、`--default-agent`、`--progress`、`--require-mention-in-group`。
3. 增加小型交互抽象，例如 `feishuAddPrompter`，生产实现使用 stdin/stdout，测试实现使用内存输入；`app_secret` 使用隐藏输入函数。
4. `runFeishuAdd` 合并 flags 和交互回答，最终调用 `runFeishuBootstrap`。
5. 运行定向测试、cmd 包测试、全量测试、vet 和 diff 检查。

## 验证结果

- RED：`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestRunFeishuAdd' -count=1 -timeout 60s` 初次失败在 `runFeishuAdd` 与 `feishuAddOptions` 未定义，符合 TDD 预期。
- GREEN：`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run 'TestRunFeishuAdd' -count=1 -timeout 60s` 通过。
- 包级验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -count=1 -timeout 60s` 通过；首次 sandbox 运行因现有 `httptest` 需要监听本地临时端口失败，提升权限复跑通过。
- 全量验证：`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s` 通过；因包含同类本地监听测试，使用提升权限执行。
- 静态验证：`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...` 通过。
- 空白检查：`git diff --check` 通过。

## 进度记录

- P0 已完成：用户已确认执行。
- 计划调整：为满足单文件行数约束，新增 `cmd/feishu_add.go` 与 `cmd/feishu_add_test.go` 承载交互逻辑和测试，`cmd/feishu.go` 只做注册级改动。
- P1 已完成：新增 `TestRunFeishuAddPromptsAndBootstrapsBot`，覆盖交互输入、凭证保存、配置更新和 secret 不泄露。
- P2/P3 已完成：新增 `weclaw feishu add`、终端交互输入器、隐藏 secret 输入和 bootstrap 复用路径。
- P4 已完成：定向测试、cmd 包测试、全量测试、vet、diff check 均已通过。

## Review 小结

终态：finished。Spec 符合度：已按方案 A 完成，`add` 只负责收集缺失字段，最终复用 `runFeishuBootstrap`，没有新增第二套配置写入路径。

安全检查：真实终端下 `app_secret` 使用隐藏输入；非 TTY 支持管道输入但不会回显；自动化测试确认输出不包含 secret。

测试与验证：定向测试、cmd 包测试、全量测试、vet 和 diff check 均通过；需要本地端口的现有测试已用提升权限执行。

复杂度检查：新增文件均小于 300 行，单函数保持 50 行以内；命令注册只在 `cmd/feishu.go` 增加最小 flag 和子命令挂载。

Document-refresh: not-needed
原因：新增命令不改变已有配置格式和运行时语义，现有 bootstrap 文档仍是脚本化入口。

剩余风险：交互式 add 默认进度模式为 `stream`，如用户要关闭可输入 `off` 或显式传 `--progress off`。

潜在技术债：多个飞书子命令继续共享全局 flag 变量，后续若扩展更多交互命令可再拆命令局部配置结构。

结论：通过。
