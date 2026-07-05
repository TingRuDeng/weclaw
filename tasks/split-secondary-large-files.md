# 次级大文件拆分计划

## 目标

继续降低剩余生产大文件的维护风险，在不改变行为的前提下，优先拆分 `agent/` 与 `messaging/` 中超过 300 行且职责边界清晰的生产文件。

## 非目标

- 不拆测试文件，例如 `messaging/handler_test.go`、`agent/acp_agent_persistence_test.go`。
- 不改变用户命令、配置格式、持久化 JSON 结构、网络访问策略或 Agent 调用语义。
- 不做功能修复、日志策略调整或新抽象设计。
- 不提交、不发布，除非用户后续明确要求。

## 当前事实

- `messaging/handler.go` 已降到 286 行，`agent/acp_agent.go` 已降到 76 行。
- 当前剩余生产大文件主要包括：
  - `messaging/progress.go`：424 行，包含 `progressSession` 生命周期、进度文案、错误友好化、通用小工具。
  - `messaging/codex_sessions.go`：402 行，包含 Codex 会话 store、binding key 规范化、workspace CRUD、状态 load/save。
  - `agent/cli_agent.go`：374 行，包含 CLI Agent 结构、Claude 会话管理、Claude 调用、Codex CLI 调用。
  - `messaging/codex_local_sessions.go`：371 行，包含 Codex App SQLite 读取、本地 JSONL session index/meta 解析、排序。
  - `messaging/linkhoard.go`：368 行，包含 URL 识别、HTML meta 提取、Jina 读取、Linkhoard 保存。
  - `messaging/codex_browser.go`：332 行，包含 `/cx` 工作空间浏览、会话列表、进入与切换辅助。
  - `agent/companion_agent.go`：304 行，仅略超 300 行，包含 Companion listener、连接管理、Agent 接口方法。
- 工作区已有上一轮拆分改动未提交，继续修改必须避免跨范围行为变更。

## 决策日志

- 优先拆生产文件，不拆测试文件：测试拆分收益低、回归面大，不适合混入当前工作区。
- 优先选择机械移动：保持函数签名、调用点和状态结构不变，降低行为差异。
- 暂不改持久化结构：`codex_sessions.go` 的 JSON schema 与 binding key 兼容逻辑不能在本轮改动。
- 采用串行执行：候选文件之间虽可独立，但当前工作区已有大量新增文件，串行更容易做 diff review。

## 执行计划

- [x] P0 串行：拆 `messaging/progress.go`
  - 保留：`progressSession` 类型与生命周期入口 `startProgressSession`、`startProgressSessionWithFinal`、`progressSession.start`、`progressSession.stopWithFinal`。
  - 新增：`messaging/progress_render.go`，移动 `renderAcceptance`、`renderInitialProgress`、`renderDeltaProgress`、`renderFinalSuccess`、`renderFinalFailure`。
  - 新增：`messaging/progress_errors.go`，移动 `friendlyAgentError`、`sanitizeAgentError`、`isCodexUpstreamError`、`isCodexWebSocketForbidden`、`isACPSessionNotFound`、`isTurnTimeoutError`。
  - 新增：`messaging/progress_utils.go`，移动 `progressModeAllowsProgress`、`progressTaskTitle`、`shouldSendProgress`、`progressTickerInterval`、`durationSeconds`、`boolValue`、`firstNonBlank`、`truncateTailRunes`。
- [x] P1 串行：拆 `messaging/codex_sessions.go`
  - 保留：`codexSessionStore` 类型、构造器、公开默认路径、基础 getter/setter。
  - 新增：`messaging/codex_session_keys.go`，移动 `codexBindingKey`、`migrateLegacyBindingKey`、`normalizeConversationUserKey`、`buildCodexConversationID`、`normalizeCodexWorkspaceRoot`。
  - 新增：`messaging/codex_session_persistence.go`，移动 `load`、`save`、`mergeCodexSessionBinding`、`mergeCodexWorkspaceSession`。
  - 新增：`messaging/codex_session_workspace.go`，移动 workspace list/clean/find/update 相关方法。
- [x] P2 串行：拆 `agent/cli_agent.go`
  - 保留：`CLIAgent`、`CLIAgentConfig`、构造器、接口方法、cwd/session 小方法。
  - 新增：`agent/cli_claude.go`，移动 `chatClaude` 与 Claude stream 解析类型。
  - 新增：`agent/cli_codex.go`，移动 `chatCodex`。
  - 新增：`agent/cli_process.go`，移动 `turnKillGrace` 与 `configureTurnProcess`。
- [x] P3 串行：视行数继续拆 `messaging/codex_local_sessions.go`、`messaging/linkhoard.go`、`messaging/codex_browser.go`、`agent/companion_agent.go`
  - 仅在 P0-P2 验证通过后执行。
  - 每个文件只按天然边界拆，不做跨模块抽象。
- [x] P4 串行：统一运行验证和 review-gate。

## 验证矩阵

- `gofmt -w` 受影响 Go 文件。
- `git diff --check`。
- `python3 scripts/validate_docs.py . --profile generic`。
- `PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`。
- `GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`。
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`。

## 风险与预想失败场景

- 机械移动漏掉 import：通过 `go test` 和 `go vet` 捕获。
- 旧测试依赖未导出函数同包访问：保持 package 不变，不改函数名。
- 大量新增文件导致 review 成本上升：每个文件只承载一个明确职责。
- 本地测试需要监听端口或写用户状态目录：沙箱失败时按权限规则用非沙箱重跑验证。

## 进度记录

- [x] 已完成只读现状分析。
- [x] 用户已确认 HARD-GATE。
- [x] P0 已完成：`messaging/progress.go` 拆出 `progress_render.go`、`progress_errors.go`、`progress_utils.go`；`go test ./messaging` 非沙箱重跑通过。
- [x] P1 已完成：`messaging/codex_sessions.go` 拆出 `codex_session_keys.go`、`codex_session_workspace.go`、`codex_session_persistence.go`；`go test ./messaging` 非沙箱重跑通过。
- [x] P2 已完成：`agent/cli_agent.go` 拆出 `cli_process.go`、`cli_claude.go`、`cli_codex.go`；`go test ./agent` 非沙箱重跑通过。
- [x] P3 已完成：`messaging/codex_local_sessions.go`、`messaging/linkhoard.go`、`messaging/codex_browser.go`、`agent/companion_agent.go` 已按职责拆分；`go test ./agent ./messaging` 非沙箱重跑通过。
- [x] P4 已完成：全量静态检查、文档契约校验、vet 和测试均通过。

## Review 小结

已完成次级生产大文件零行为拆分：`messaging/progress.go` 拆出进度渲染、错误友好化和工具函数；`messaging/codex_sessions.go` 拆出 binding key、workspace 操作和持久化；`agent/cli_agent.go` 拆出进程配置、Claude CLI 调用和 Codex CLI 调用；`messaging/codex_local_sessions.go` 拆出 Codex App 侧会话读取；`messaging/linkhoard.go` 拆出 HTML 元数据、Jina Reader 和 Linkhoard 保存；`messaging/codex_browser.go` 拆出工作空间聚合和会话显示辅助；`agent/companion_agent.go` 拆出 Agent 生命周期接口方法。生产 Go 文件当前均低于 300 行，剩余超过 300 行的是既有测试文件。验证命令：`git diff --check`、`python3 scripts/validate_docs.py . --profile generic`、`PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`，结果均通过；沙箱内测试因本地端口监听和用户状态目录写入受限失败，已按权限规则非沙箱重跑通过；`py_compile` 生成的临时 pyc 文件已清理。
