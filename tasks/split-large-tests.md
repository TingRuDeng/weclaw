# 测试大文件拆分计划

## 目标

在不改变测试语义的前提下，拆分剩余超大测试文件，降低后续审查、定位和新增测试的成本。

## 非目标

- 不修改生产代码。
- 不改变测试断言、测试数据、测试 helper 行为。
- 不引入新的 mock 框架或测试运行策略。
- 不跳过、删除或弱化任何现有测试。
- 不提交、不发布，除非用户后续明确要求。

## 当前事实

- 当前分支：`main...origin/main`；用户已确认执行，工作区包含本轮测试拆分改动。
- 当前最大测试文件：
  - `messaging/handler_test.go`：4959 行。
  - `agent/acp_agent_persistence_test.go`：1401 行。
  - `agent/approval_test.go`：429 行。
- `messaging/handler_test.go` 里同时包含：
  - 测试桩：`fakeAgent`、`fakeCodexThreadAgent`、`blockingProgressAgent`、`blockingCodexThreadAgent`、`recordedILinkCalls`。
  - 命令解析测试：`TestParseCommand_*`、`TestResolveAlias`、`TestBuildHelpText`。
  - 进度测试：`TestStartProgressSession*`、`TestSendToNamedAgent*`。
  - 审批测试：`TestApprovalHandlerWaitsForChoice`、`TestPendingApproval*`、`TestExpiredApproval*`。
  - Codex 会话测试：`TestHandleCodex*`、`TestCodexCx*`、`TestCodexAttach*`、`TestCodexStatus*`。
  - 本地会话扫描 helper：`writeLocalCodexSession*`、`writeLocalClaudeSession*`。
- `agent/acp_agent_persistence_test.go` 里同时包含：
  - ACP/Codex 状态持久化测试。
  - thread start/resume/reset 测试。
  - Codex quota/model 测试。
  - Codex event/final assembler/error/rehydrate 测试。
  - helper server 与状态文件 helper。
- 当前环境 `approval policy=never`，全量测试在沙箱内会因 `httptest` 本地监听和 `~/.weclaw/state` 写入被拒绝；可运行编译级测试和静态检查，完整测试需要后续在允许非沙箱时执行。

## 决策日志

- 优先拆测试 helper，再拆测试用例：helper 先独立出来，后续移动用例时 diff 更清晰。
- 优先拆 `messaging/handler_test.go`：它最大且覆盖面最广，是当前最高维护成本来源。
- `agent/acp_agent_persistence_test.go` 第二阶段处理：ACP 相关测试和 helper 依赖更集中，适合在 messaging 测试拆完后单独拆。
- `agent/approval_test.go` 暂不优先处理：429 行虽超过 300，但结构相对集中，收益低于前两个文件。
- 采用串行执行：同包测试 helper 和用例互相引用密集，不适合并行写文件。

## 执行计划

- [x] P0 串行：拆 `messaging/handler_test.go` 的通用测试桩
  - 新增 `messaging/handler_test_fakes_test.go`。
  - 移动 `fakeAgent`、`fakeCodexThreadAgent`、`fakeVisibleCodexAgent`、`fakeClaudeSessionAgent`、`fakeProgressAgent`、`blockingProgressAgent`、`blockingCodexThreadAgent`。
  - 移动 `newTestHandler`、`newRecordingILinkClient`、`recordedILinkCalls`、`waitForText`、`waitForFakeAgentCalls` 等通用 helper。
- [x] P1 串行：拆 `messaging/handler_test.go` 的命令和状态类测试
  - 新增 `messaging/handler_command_test.go`。
  - 移动 `TestParseCommand_*`、`TestResolveAlias`、`TestBuildHelpText`、`TestStatusCommand*`、`TestHandleProgressCommand*`。
- [x] P2 串行：拆 `messaging/handler_test.go` 的进度、任务和审批测试
  - 新增 `messaging/handler_progress_task_test.go`。
  - 新增 `messaging/handler_approval_test.go`。
  - 移动 `TestStartProgressSession*`、`TestSendToNamedAgent*`、`TestBroadcast*`、`TestPendingApproval*`、`TestExpiredApproval*`。
- [x] P3 串行：拆 `messaging/handler_test.go` 的 Codex / Claude 会话测试
  - 新增 `messaging/handler_codex_session_test.go`。
  - 新增 `messaging/handler_claude_session_test.go`。
  - 移动 `TestHandleCodex*`、`TestCodexCx*`、`TestCodexAttach*`、`TestCodexStatus*`、`TestDiscoverLocalCodex*`、`TestDiscoverLocalClaude*`。
  - 移动本地 Codex/Claude 测试文件写入 helper。
- [x] P4 串行：拆 `agent/acp_agent_persistence_test.go`
  - 新增 `agent/acp_state_helpers_test.go`，移动 state file helper。
  - 新增 `agent/acp_thread_test.go`，移动 thread start/resume/reset/controls 测试。
  - 新增 `agent/acp_recovery_test.go`，移动 reset、fallback、rehydrate 测试。
  - 新增 `agent/acp_runtime_test.go`、`agent/acp_codex_delta_test.go`、`agent/acp_codex_event_test.go`，移动 runtime、delta/assembler、error/limit 测试。
  - 新增 `agent/acp_model_quota_test.go`，移动 quota/model 测试。
- [x] P5 串行：视行数处理 `agent/approval_test.go`
  - 新增 `agent/approval_response_test.go`。
  - 保留 `agent/approval_test.go` 负责审批请求解析，响应/策略测试移入新文件。
- [x] P6 串行：拆 `feishu` 包剩余偏大的测试文件
  - 拆分 `feishu/adapter_test.go`、`feishu/replier_test.go`、`feishu/choice_test.go`。
  - 按审批卡片、幂等、过期提示、SDK 回复、已处理选择等主题拆分。
- [x] P7 串行：拆 `cmd/start_test.go`
  - 新增 `cmd/start_agent_test.go`，移动 Codex/Companion agent 创建测试和 helper。
  - 新增 `cmd/start_runtime_test.go`，移动 stop/runtime lock/pid 状态测试。
  - 保留 `cmd/start_test.go` 负责平台开关和 registry 测试。
- [x] P8 串行：拆 `cmd/codex_app_companion_test.go`
  - 新增 `cmd/codex_app_client_test.go`，移动 WebSocket client 协议测试和 helper。
  - 新增 `cmd/codex_app_runtime_test.go`，移动 runtime/args 测试和 fake client。
- [x] P9 串行：拆 `config/config_test.go`
  - 新增 `config/progress_test.go`，移动进度配置测试。
  - 新增 `config/permission_test.go`，移动权限派生测试。
- [x] P10 串行：运行验证和 review-gate。

## 验证矩阵

- `gofmt -w` 受影响测试文件。
- `git diff --check`。
- `python3 scripts/validate_docs.py . --profile generic`。
- `PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`。
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./agent ./messaging -run TestNonExistent -count=1 -timeout 60s`。
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd ./config ./feishu -run TestNonExistent -count=1 -timeout 60s`。
- `GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`。
- 若环境允许非沙箱：`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`。
- 若当前环境仍为 `approval policy=never`：记录全量测试因本地监听 / 用户状态目录权限失败，不能伪造通过。

## 风险与预想失败场景

- 移动 helper 漏 import：编译级测试和 `go vet` 捕获。
- 同名 helper 移动后文件顺序无关，但 import 变化可能造成未使用：`gofmt` 和 `go test -run TestNonExistent` 捕获。
- 大量测试移动导致 review 困难：按主题分阶段移动，每阶段验证。
- 当前环境不能非沙箱测试：只记录环境限制，不跳过验证结论。

## 进度记录

- [x] 已完成只读现状分析。
- [x] 用户已确认 HARD-GATE。
- [x] P0 已完成：新增 `messaging/handler_test_fakes_test.go`，`messaging` 包编译级测试通过。
- [x] P1 已完成：新增 `messaging/handler_command_test.go`、`messaging/handler_command_status_test.go`，命令、帮助、状态、进度配置测试编译通过。
- [x] P2 已完成：新增 `messaging/handler_progress_task_test.go`、`messaging/handler_approval_test.go`，进度、任务、审批测试编译通过。
- [x] P3 已完成：新增 `messaging/handler_codex_session_test.go`、`messaging/handler_codex_browse_test.go`、`messaging/handler_codex_entry_test.go`、`messaging/handler_claude_session_test.go`，Codex/Claude 会话测试编译通过。
- [x] P4 已完成：拆分 ACP 测试为 thread、recovery、rehydrate、runtime、delta、event、model/quota 和 state helper 文件，`agent` 包编译通过。
- [x] P5 已完成：新增 `agent/approval_response_test.go`，审批请求解析与响应/策略测试拆分后 `agent` 包编译通过。
- [x] P6 已完成：拆分飞书 adapter、replier、choice 测试文件，`feishu` 包编译通过。
- [x] P7 已完成：拆分 `cmd/start_test.go` 为 agent、platform、runtime 三类测试文件，`cmd` 包编译通过。
- [x] P8 已完成：拆分 `cmd/codex_app_companion_test.go` 为 message、client、runtime 三类测试文件，`cmd` 包编译通过。
- [x] P9 已完成：拆分 `config/config_test.go` 为基础配置、progress、permission 三类测试文件，`config` 包编译通过。
- [x] P10 已完成：文档校验、受影响包编译级测试、`go vet ./...`、`git diff --check` 均通过；全量 `go test ./...` 在当前沙箱因本地监听和 `~/.weclaw/state` 写入权限失败。

## Review 小结

终态：finished。

Spec 符合度：通过。本轮只拆测试文件和任务记录，不修改生产代码，不改变断言或 helper 行为。

安全检查：通过。未新增 secret、外部输入处理、网络访问或权限放宽逻辑。

测试与验证：通过最小充分验证。`PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`、`python3 scripts/validate_docs.py . --profile generic`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./agent ./messaging ./feishu ./cmd ./config -run TestNonExistent -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`git diff --check` 均通过。`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s` 失败，原因是当前沙箱禁止 `httptest`/companion 监听本地端口，并拒绝写 `/Users/dengtingru/.weclaw/state/*.tmp`。

复杂度检查：通过。原 `messaging/handler_test.go`、`agent/acp_agent_persistence_test.go`、`agent/approval_test.go`、`feishu/adapter_test.go`、`cmd/start_test.go`、`cmd/codex_app_companion_test.go`、`config/config_test.go` 已拆散；当前最高测试文件为 `agent/acp_thread_test.go` 300 行、`messaging/handler_task_guide_test.go` 296 行、`feishu/replier_test.go` 291 行。

Document-refresh: not-needed
原因：本轮是测试文件结构调整，未改变业务模块、命令、配置或发布流程。

剩余风险：全量测试未能在当前沙箱完成，需要在允许本地监听和用户状态目录写入的环境复跑。

潜在技术债：本轮未发现需要继续处理的测试文件体量技术债。

结论：通过。
