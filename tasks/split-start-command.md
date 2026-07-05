# 拆分 cmd/start.go 计划

## 目标

在不改变启动行为的前提下，拆分 `cmd/start.go` 的职责边界，降低 daemon、平台 registry、agent factory、软配置重载互相耦合带来的回归风险。

## 非目标

- 不改变 `weclaw start`、`weclaw stop`、后台 daemon、登录、平台启动和 API 启动行为。
- 不新增配置项。
- 不修复新业务问题。
- 不提交、不发布，除非用户后续明确要求。

## 当前事实

- 当前分支：`main...origin/main`。
- 当前工作区已有上一轮测试拆分的大量未提交改动，不能执行 `git pull`，否则会混入合并风险。
- `cmd/start.go` 当前 789 行，是最大生产文件。
- `cmd/start.go` 当前职责包含：
  - Cobra 命令入口：`init`、`startCmd`、`runStart`。
  - 平台 registry：`buildPlatformRegistry`、`wechatEnabled`、`wechatAggregationWindow`。
  - agent factory：`createAgentByName`、`startACPAgentWithRetry`、`newACPAgentFromConfig`、`isCodexAppServerAgent`、`isRetryableCodexStateRuntimeError`、`companionAutoLaunchEnabled`。
  - 进度和平台配置提取：`extractAgentProgressConfigs`、`extractPlatformProgressConfigs`、`extractPlatformDefaultAgents`。
  - 软配置重载：`runSoftConfigReloader`、`applySoftConfig`。
  - 微信登录：`doLogin`。
  - daemon 与进程控制：`weclawDir`、`pidFile`、`daemonLaunchLockFile`、`logFile`、`runDaemon`、`waitDaemonChildReady`、`handleDaemonPIDWriteResult`、`processExists`、`stopAllWeclawWithOps` 等。
- 现有相关测试已拆分为：
  - `cmd/start_test.go`：平台开关和 registry。
  - `cmd/start_agent_test.go`：agent 创建和 Codex ACP retry。
  - `cmd/start_runtime_test.go`：runtime lock、pid、stop 流程。

## 决策日志

- 采用“同包拆文件”方案，不改变包名，不引入新接口。
- 优先按现有测试文件主题拆生产文件，避免测试与实现职责错位。
- `runStart` 暂时保留在 `cmd/start.go`，因为它是入口编排函数；这轮只把被编排的子职责移出。
- 不并行执行。当前工作区已有大量未提交测试文件迁移，且拆分同一包内生产文件会共享 import 和 helper，串行更清晰。

## 方案对比

### 方案 A：同包按职责拆文件

- 新增 `cmd/start_platform.go`，移动平台 registry 和微信平台开关函数。
- 新增 `cmd/start_agent.go`，移动 agent factory 和 ACP retry 辅助函数。
- 新增 `cmd/start_config_reload.go`，移动进度配置提取与软配置重载。
- 新增 `cmd/start_login.go`，移动微信扫码登录流程。
- 新增 `cmd/start_daemon.go`，移动 daemon、pid、锁和 stop 进程控制函数。
- 保留 `cmd/start.go` 只放 flag、cobra 命令和 `runStart` 主编排。

优点：最小影响，不改 API，不改测试语义；能把最大文件直接压到较小范围。  
缺点：仍然是 `cmd` 包内函数，边界不是强类型隔离。

### 方案 B：抽独立子包

- 把 daemon、platform registry、agent factory 拆到 `cmd/startinternal` 或 `internal/start`。

优点：边界更强。  
缺点：会改变大量未导出函数可见性，测试需要跨包调整；当前工作区已有大规模测试拆分，继续扩大影响面不划算。

## 推荐方案

选择方案 A。

淘汰方案 B 的原因：当前目标是降低大文件维护成本，不是重塑启动架构。跨包迁移会制造额外导出符号和测试改动，风险高于收益。

## 风险与预想失败场景

- import 漏删或漏加：通过 `go test ./cmd -run TestNonExistent` 和 `go vet ./...` 捕获。
- 移动函数后测试文件引用仍应正常：同包拆文件不改变函数可见性。
- `runStart` 对 helper 的调用顺序不能变化：本轮只移动函数位置，不改函数体。
- 当前沙箱不能跑完整测试：继续记录 `httptest` 本地监听和用户状态目录写入限制，不伪造全量通过。

## 执行计划

- [x] P0 串行：移动平台 registry 相关函数
  - 新增 `cmd/start_platform.go`。
  - 移动 `buildPlatformRegistry`、`wechatEnabled`、`wechatAggregationWindow`。
  - 验证 `GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run TestNonExistent -count=1 -timeout 60s`。
- [x] P1 串行：移动 agent factory 相关函数
  - 新增 `cmd/start_agent.go`。
  - 移动 `createAgentByName`、`startACPAgentWithRetry`、`newACPAgentFromConfig`、`isCodexAppServerAgent`、`isRetryableCodexStateRuntimeError`、`companionAutoLaunchEnabled`。
  - 验证 `GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run TestNonExistent -count=1 -timeout 60s`。
- [x] P2 串行：移动软配置重载相关函数
  - 新增 `cmd/start_config_reload.go`。
  - 移动 `extractAgentProgressConfigs`、`extractPlatformProgressConfigs`、`extractPlatformDefaultAgents`、`runSoftConfigReloader`、`applySoftConfig`。
  - 验证 `GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run TestNonExistent -count=1 -timeout 60s`。
- [x] P3 串行：移动登录流程
  - 新增 `cmd/start_login.go`。
  - 移动 `doLogin`。
  - 验证 `GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run TestNonExistent -count=1 -timeout 60s`。
- [x] P4 串行：移动 daemon 和进程控制
  - 新增 `cmd/start_daemon.go`。
  - 移动 `weclawDir`、`pidFile`、`daemonLaunchLockFile`、`logFile`、daemon 常量、`runDaemon`、`waitDaemonChildReady`、`daemonPIDWriteProcess`、`handleDaemonPIDWriteResult`、`processExists`、`stopProcessOps`、`stopAllWeclaw`、`defaultStopProcessOps`、`stopAllWeclawWithOps`、`waitProcessExit`、`signalPID`。
  - 验证 `GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run TestNonExistent -count=1 -timeout 60s`。
- [x] P5 串行：统一验证和 review-gate
  - 运行文档校验、受影响包编译级测试、`go vet ./...`、`git diff --check`。
  - 统计 `cmd/start*.go` 行数。
  - 更新本任务文件 review 小结。

## 验证矩阵

- `gofmt -w cmd/start*.go`。
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./cmd -run TestNonExistent -count=1 -timeout 60s`。
- `PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`。
- `python3 scripts/validate_docs.py . --profile generic`。
- `GOCACHE=/private/tmp/weclaw-go-cache go test ./agent ./messaging ./feishu ./cmd ./config -run TestNonExistent -count=1 -timeout 60s`。
- `GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`。
- `git diff --check`。
- 当前沙箱若继续禁止本地监听和用户状态目录写入，则全量 `go test ./...` 只记录失败原因，不作为伪通过。

## 进度记录

- [x] 已完成只读现状分析。
- [x] 用户已确认 HARD-GATE。
- [x] P0 已完成：新增 `cmd/start_platform.go`，移动平台 registry 和微信平台开关函数，`cmd` 包编译级测试通过。
- [x] P1 已完成：新增 `cmd/start_agent.go`，移动 agent factory、ACP retry 和 Codex app-server 判断函数，`cmd` 包编译级测试通过。
- [x] P2 已完成：新增 `cmd/start_config_reload.go`，移动 progress 配置提取和软配置重载函数，`cmd` 包编译级测试通过。
- [x] P3 已完成：新增 `cmd/start_login.go`，移动微信扫码登录流程，`cmd` 包编译级测试通过。
- [x] P4 已完成：新增 `cmd/start_daemon.go`，移动 daemon、pid、runtime lock 和 stop 进程控制函数，`cmd` 包编译级测试通过。
- [x] P5 已完成：文档校验、受影响包编译级测试、`go vet ./...`、`git diff --check` 均通过；全量 `go test ./...` 在当前沙箱因本地监听和 `~/.weclaw/state` 写入权限失败。

## Review 小结

终态：finished。

Spec 符合度：通过。本轮只按职责拆分 `cmd/start.go`，未改变 `runStart` 调用顺序、daemon 行为、平台 registry 行为或 agent 创建逻辑。

安全检查：通过。未新增 secret、权限放宽、外部输入处理、shell 拼接或静默 fallback。

测试与验证：通过最小充分验证。`PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`、`python3 scripts/validate_docs.py . --profile generic`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./agent ./messaging ./feishu ./cmd ./config -run TestNonExistent -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`git diff --check` 均通过。`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s` 失败，原因是当前沙箱禁止 `httptest`/companion 监听本地端口，并拒绝写 `/Users/dengtingru/.weclaw/state/*.tmp`。

复杂度检查：通过。`cmd/start.go` 从 789 行降到 220 行；新增文件中最大的是 `cmd/start_daemon.go` 240 行，均低于 300 行。

Document-refresh: not-needed
原因：本轮是启动命令内部文件拆分，未改变用户命令、配置字段、发布流程或文档契约。

剩余风险：全量测试未能在当前沙箱完成，需要在允许本地监听和用户状态目录写入的环境复跑。

潜在技术债：`cmd/start.go` 已完成第一层职责拆分；后续若继续降低耦合，可单独评估 daemon 进程控制是否需要独立内部包，但本轮不扩大范围。

结论：通过。
