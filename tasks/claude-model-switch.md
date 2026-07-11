# Claude 模型与推理强度切换

## 目标

- `/model`、`/reasoning` 根据当前消息会话选择的 Agent 操作 Codex 或 Claude。
- Claude CLI 和 Claude ACP 都支持运行时设置模型与推理强度。
- 设置保持 Agent 级作用域，只用于后续创建的新会话。
- 飞书使用动态选择卡片，微信保留文本命令。

## 非目标

- 不重复实现 `d447ec2` 已完成的会话 Agent 持久化与路由。
- 不让 `/cc <内容>`、`/cx <内容>` 改变当前会话 Agent。
- 不修改已有 Claude/Codex session 的模型或推理强度。
- 不把 Claude 设置写回 `config.json`。

## 当前事实

- `messaging/agent_session_store.go` 已持久化每个 `routeUserID` 的当前 Agent。
- `messaging/model_command.go` 已按 `modelAgentRoute` 解析当前 Agent，但只接受 `CodexModelControlAgent`。
- Claude CLI 已使用 `--model`，但未接入 `effort`，且运行时没有 setter。
- Claude ACP 官方能力矩阵不支持 `session/set_model`；官方实现通过 `session/set_config_option` 的 `model`、`effort` 配置新 session。
- `agent/acp_types.go` 的 `newSessionResult` 目前只解析 `sessionId`，没有解析 `configOptions`。

## 决策日志

- 保留 Codex、Claude 专用 Agent 接口，在 messaging 层使用统一适配器，避免重构 Codex 额度等专属能力。
- Claude CLI 为每个 WeClaw conversation 捕获创建时的模型/effort；已绑定 session 不接受后续全局切换。
- Claude ACP 只在 `session/new` 后设置模型和 effort；已有 session 不调用配置方法。
- Claude ACP 明确配置失败时直接终止新 session 创建，不做静默降级。
- Claude 模型目录沿用现有内置目录；推理档位使用当前 Claude CLI 声明的 `low`、`medium`、`high`、`xhigh`、`max`。
- 不使用 subagent；接口、运行时状态和消息适配连续依赖，串行 TDD 避免写冲突。

## 执行计划

- [x] P1 RED：补 Claude CLI 模型/effort 状态、setter、会话配置捕获测试，并确认失败。
- [x] P2 GREEN：实现 Claude CLI 运行时控制和新会话配置隔离，接入启动配置 `effort`。
- [x] P3 RED：补 Claude ACP `configOptions` 解析、`session/set_config_option` 顺序、已有 session 不变和错误暴露测试。
- [x] P4 GREEN：实现 Claude ACP 模型控制和新 session 配置。
- [x] P5 RED/GREEN：补当前会话 Claude/Codex 卡片与文本命令测试，实现 messaging 双 Agent 适配器。
- [x] P6 验证：执行定向测试、受影响包 race、全仓测试、vet、漏洞扫描、文档契约和 Review Gate。

## 验证矩阵

- Claude CLI 新会话携带捕获的 `--model`、`--effort`，旧会话保持创建时配置。
- Claude ACP 新 session 先设置 model、再设置 effort；旧 session 不调用配置方法。
- Claude ACP 配置值无效或协议不支持时返回明确错误。
- 当前会话为 Claude 时 `/model`、`/reasoning` 展示和修改 Claude；切回 Codex 后操作 Codex。
- 飞书无参数命令使用卡片，微信和显式参数命令使用文本。
- 非 Codex/Claude Agent 继续返回配置固定提示。

## 进度记录

- 2026-07-11：用户确认模型与 effort、会话级 Agent 选择持久化、Agent 级设置作用域，以及仅裸 `/cc`、`/cx` 切换当前 Agent。
- 2026-07-11：基于最新提交 `d447ec2` 重新核实，移除已完成的会话 Agent 存储与路由工作。
- 2026-07-11：P1/P2 完成；Claude CLI 定向测试先因缺少 effort、setter 和会话捕获失败，最小实现后通过。
- 2026-07-11：P3/P4 完成；Claude ACP 测试隔离本机状态后通过，覆盖配置顺序、旧 session 不变、失败不落库和动态模型缓存。
- 2026-07-11：P5 完成；双 Agent 卡片测试先因 Codex 专用断言失败，实现适配器后通过，并修复 `/reasoning` 先误写模型的原有路由缺陷。
- 2026-07-11：P6 完成；Claude CLI 长函数按职责拆分，最终 race、全仓测试、vet、漏洞扫描、文档契约和差异检查通过。

## Review 小结

终态：finished。

Spec 符合度：通过。`/model`、`/reasoning` 按会话当前 Agent 分流；Claude CLI 和 ACP 均支持模型与 effort；已有 session 不被运行时切换覆盖。

安全检查：通过。卡片选项来自 Agent 模型目录；ACP 显式配置失败直接返回且不保存 session；未新增凭据、Shell 拼接或静默降级路径。

测试与验证：TDD 的 CLI、ACP、messaging 主路径均先失败后通过；最终 `go test -race ./agent ./cmd ./messaging -count=1 -timeout 60s`、`go test ./... -count=1 -timeout 120s`、`go vet ./...`、`govulncheck`、文档契约和 `git diff --check` 均通过。

复杂度检查：通过。新增和修改文件均低于 300 行；`chatClaude` 已拆分为参数构造、进程启动、流解析、退出处理和 session 保存函数，相关函数均低于 50 行。

Document-refresh: not-needed

原因：公开命令名称和配置字段未新增；`effort` 仅从 Codex 专用扩展为 Claude 同字段复用，现有 `/model`、`/reasoning` 帮助语义保持一致。

剩余风险：Claude CLI 内置模型目录与 effort 档位依赖当前 Claude Code CLI 契约；Claude ACP 在首次创建 session 前只能使用内置目录，创建后会切换为 ACP 动态目录。

潜在技术债：Claude ACP 的动态模型目录仅保存在进程内；WeClaw 重启后要等下一次 Claude ACP session 创建才能重新获得实时目录。

结论：通过。
