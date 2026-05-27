# Companion 多运行时支持计划

## 目标

- 让 Companion 模式优先支持 `opencode` 与 `codex` 两类可见本地运行时。
- 保留核心体验：远程微信发消息，本地 Codex / OpenCode 终端保持可见，并接入同一后台运行时。
- 复用现有 Companion socket 协议，不为不同 CLI 复制后台 Agent。

## 非目标

- 本轮不实现任意第三方 CLI 的完全自定义协议。
- 本轮不实现 Claude Companion；Claude 缺少本机可验证的 server/attach 能力，后续单独评估。
- 本轮不改 `/sw` 账号切换链路，只保证 Codex Companion 不破坏现有 Codex ACP/CLI 路径。

## 当前事实

- 证据：`agent/companion_agent.go:Start` 已经把后台 Companion 入口写到本地 endpoint 文件，协议不绑定 OpenCode。
- 证据：`agent/companion_client.go:RunCompanionClient` 已经把本地 Companion 请求转发给 `CompanionRequestHandler`，运行时可替换。
- 证据：`cmd/companion.go:createCompanionRuntime` 当前只支持 `opencode`，其他 Agent 会返回“不支持”错误。
- 证据：`cmd/opencode_companion.go:HandleCompanionRequest` 使用 `opencode serve`、`opencode attach`、`prompt_async` 和 SSE 实现可见终端与远程请求桥接。
- 证据：`agent/cli_agent.go:chatClaude` 已经有 Claude `-p --output-format stream-json --verbose` 的结构化解析和 session 续接逻辑。
- 证据：`agent/cli_agent.go:chatCodex` 当前只跑 `codex exec`，没有多轮 session 续接，也没有可见终端输出。
- 证据：`config/detect.go:agentCandidates` 当前 Claude/Codex 优先走 ACP 或 CLI，只有 OpenCode 默认走 Companion。
- 证据：本机 `codex app-server --listen ws://127.0.0.1:45678` 可接受 `initialize`、`thread/start`、`turn/start`，并通过 `item/agentMessage/delta` 与 `turn/completed` 回流文本；同一连接会收到其他 thread 事件，正式实现必须按 `threadId` 过滤。
- 证据：本机 `codex --help` 支持 `--remote <ADDR>`，可让可见 TUI 连接远程 app-server websocket。

## 决策日志

- 2026-05-27：OpenCode 继续保留专用 server runtime，因为它有明确的 `serve/attach/event` API。
- 2026-05-27：用户进一步确认目标是“远程微信控制 + 本地 Codex app 可见终端继续存在”，因此 Codex 从命令执行 runtime 调整为 app-server websocket runtime。
- 2026-05-27：Codex 不默认从 ACP 切到 Companion，避免破坏 `/codex ls`、`/codex switch`、模型查询和现有 thread 管理。

## 方案对比

### 方案 A：Codex 使用 app-server websocket runtime

- 做法：Companion 启动 `codex app-server --listen ws://127.0.0.1:<port>`，再启动可见 `codex --remote ws://127.0.0.1:<port>`；微信消息通过同一 websocket 发送 `turn/start`。
- 优点：真正保留本地 Codex 可见终端，并让微信消息进入同一 app-server。
- 缺点：需要维护 Codex app-server JSON-RPC 子集，且事件广播必须过滤 thread。
- 结论：本轮采用。

### 方案 B：Codex 使用通用可见命令 runtime

- 做法：抽象 `localExecCompanionRuntime`，每次微信请求在 Companion 终端里启动一次 CLI 子进程，stdout/stderr 同步显示到终端，同时捕获结构化输出作为微信回复。
- 优点：复用已有 Claude/Codex CLI 能力；本地终端始终存在并可见；不依赖未确认的私有协议。
- 缺点：不是长期驻留 TUI；Claude/Codex 每条消息仍是一次非交互命令，但可通过 session id 或 resume 保留上下文。
- 结论：作为回退方案记录，但本轮不采用，避免偏离用户要的 Codex app 可见终端。

### 方案 C：PTY 注入真实交互式 TUI

- 做法：Companion 启动真实 `claude`/`codex` TUI，通过 PTY 写入微信 prompt，并从终端输出中解析回复。
- 优点：最接近“同一个本地 CLI 窗口”。
- 缺点：输出边界、全屏控制序列、权限弹窗、输入焦点都难可靠解析；很容易引入假成功或静默吞错。
- 结论：本轮不采用。

## 推荐方案

采用方案 A：保留 OpenCode 专用 runtime，新增 Codex app-server websocket runtime。

## 执行计划

- [x] P1 串行：为 `cmd/companion.go:createCompanionRuntime` 增加 `codex` 分支红灯测试。
- [x] P2 串行：新增 Codex app-server JSON-RPC websocket 客户端红灯测试，覆盖 thread 过滤、delta 聚合、错误事件透出。
- [x] P3 串行：新增 `cmd/codex_app_companion.go`，启动 app-server、连接 websocket、创建 thread、发送 turn、收集回复。
- [x] P4 串行：新增可见 TUI 启动逻辑，执行 `codex --remote ws://127.0.0.1:<port> --cd <cwd>` 并继承本地 stdin/stdout/stderr。
- [x] P5 串行：保留 OpenCode runtime 不变，确认 `createCompanionRuntime` 同时支持 `opencode` 与 `codex`。
- [x] P6 串行：更新 `README.md`、`README_CN.md` 的 Companion 配置示例，说明 Codex app-server 模式和显式配置方式。
- [x] P7 串行：运行定向测试、全量测试、静态检查、diff 检查和构建。
- [x] P8 串行：执行 review-gate，并把验证结果写回本文件。

## 文件级改动计划

- `cmd/companion.go`
  - 修改函数：`createCompanionRuntime(endpoint agent.CompanionEndpoint) (companionRuntime, error)`
  - 目标：根据 `endpoint.Agent` 分发到 `opencode`、`claude`、`codex`。

- `cmd/codex_app_companion.go`
  - 新增类型：`codexAppCompanionRuntime`
  - 新增函数：`newCodexAppCompanionRuntime(endpoint agent.CompanionEndpoint) *codexAppCompanionRuntime`
  - 新增方法：`HandleCompanionRequest(ctx context.Context, req agent.CompanionRequest, progress func(string)) (string, error)`
  - 目标：实现 Codex app-server Companion。

- `cmd/codex_app_protocol.go`
  - 新增类型：`codexAppClient`
  - 新增函数：`newCodexAppClient(url string) *codexAppClient`
  - 新增方法：`Initialize`、`StartThread`、`StartTurn`、`WaitTurn`
  - 目标：封装 Codex app-server websocket JSON-RPC 子集。

- `config/detect.go`
  - 暂不默认修改 Claude/Codex 检测优先级。
  - 如需显式 Companion，由用户在配置中设置 `"type": "companion"`。

- `README.md`、`README_CN.md`
  - 更新 Companion 示例和边界说明。

## 验证矩阵

- `go test ./cmd -run 'TestCreateCompanionRuntime|TestCodexAppCompanion' -count=1 -timeout 60s`
- `go test ./agent ./cmd ./config -run 'TestCompanion|TestDetectAndConfigure' -count=1 -timeout 60s`
- `go test -count=1 -timeout 60s ./...`
- `go vet ./...`
- `git diff --check`
- `go build -o weclaw .`
- `./weclaw companion --help | head -n 40`

## 风险与失败场景

- Claude 本机不可用，且用户最新目标聚焦 Codex app；本轮不实现 Claude，避免无证据设计。
- Codex app-server 事件会广播其他 thread；实现必须严格按 `threadId` 过滤。
- Codex app-server schema 可能随版本变化；实现只使用已验证的最小方法集，并在协议错误时暴露真实错误。
- 如果用户后续要求真实 TUI 注入，需要单独评估 PTY 和输出边界，不应混入本轮。

## HARD-GATE

用户确认本计划前，不修改实现代码。确认后按 P1 到 P8 串行执行。

## 验证结果

- `go test ./cmd -run 'TestCreateCompanionRuntime|TestCodexAppCompanion|TestCodexAppClient|TestHandleCodexAppMessage|TestCodexAppCommandArgs' -count=1 -timeout 60s`：通过。
- `go test -count=1 -timeout 60s ./...`：通过。
- `go vet ./...`：通过。
- `git diff --check`：通过。
- `go build -o weclaw .`：通过。
- `./weclaw companion --help | head -n 40`：通过，输出中文命令说明。

## Review 小结

- 终态：finished。
- Spec 符合度：已按更新后的 Codex app-server Companion 方案实现，OpenCode runtime 保持不变，Codex 仍需显式配置为 `type=companion`。
- 安全检查：Codex app-server 只监听 `127.0.0.1` 随机端口；未写入 secret；websocket 事件按 `threadId` 与 `turnId` 过滤。
- 测试与验证：已覆盖 runtime 分发、命令参数构造、websocket JSON-RPC、thread 过滤、delta 聚合和错误透出，并通过全量验证。
- 复杂度检查：新增 Go 文件均未超过 300 行；核心函数保持短小，未引入静默 fallback。
- Document-refresh: needed
  原因：新增 Codex Companion 使用方式，已同步更新 README 与 README_CN。
- 剩余风险：尚未用真实微信端到端触发 Codex Companion；本轮已用本地 Codex app-server 协议实验验证 `thread/start` 与 `turn/start` 可用。
- 潜在技术债：Claude Companion 未实现，需后续基于可验证 CLI/server 能力单独设计。
- 结论：通过。
