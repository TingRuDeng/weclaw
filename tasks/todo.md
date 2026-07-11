# 当前任务记录

## 目标

按 2026-07-11 全面审查的严重级别顺序修复 P1、P2、P3 问题。每项先补失败测试，再做最小根因修复并执行受影响范围验证。

## 非目标

- 不改变现有飞书、微信命令名称和正常交互语义。
- 不重写 Agent、消息平台或配置架构。
- 不保留已确认有风险的失败放行和静默降级路径。
- 不在本轮处理无关重构或新增产品功能。

## 当前事实

- 基线分支：`main`，提交 `2ae9564`，工作分支 `codex/fix-comprehensive-review`。
- 基线全仓单测、全仓 race、`go vet`、`govulncheck`、文档校验均通过。
- 基线总语句覆盖率为 69.8%，关键并发时序缺少定向测试。
- 本轮按顺序串行执行；多个任务共享 Agent 和 Handler 状态，不使用并行写入或 subagent。

## 已确认决策

- Codex 同一真实 thread 同时只允许一个 turn 所有者；事件通道必须带注册所有权，注销时不能删除其他订阅者。
- 所有 Agent 都登记运行任务；Codex 保留暂存消息和跨端控制能力，其他 Agent 只复用统一生命周期与重启计数。
- 已知 WeClaw 进程存在但运行状态无法确认时，普通重启失败关闭；仅 `--force` 可以继续。
- loopback API 保留本地无 token 调用，但拒绝非 loopback Host 和跨源浏览器请求。
- 持久化写入必须串行、使用唯一临时文件并原子替换。

## 执行计划

- [x] P1-1 串行：修复 Codex thread 事件通道覆盖和错误注销。
  - 修改：`agent/acp_agent.go`、`agent/codex_turn_dispatch.go`、`agent/codex_app_server_turn.go`、`agent/codex_thread_watch.go`、`agent/acp_chat.go`。
  - 测试：新增同 key 重复注册、所有权注销和同 thread 并发 turn 回归测试。
- [x] P1-2 串行：统一 Codex、Claude、HTTP Agent 运行任务生命周期。
  - 修改：`messaging/agent_execution.go`、`messaging/task_state.go`、`messaging/handler_status.go`。
  - 测试：Claude/HTTP 执行期间 `ActiveTaskCount()` 为 1，结束后归零。
- [x] P1-3 串行：重启状态读取失败时默认拒绝重启。
  - 修改：`cmd/restart_safety.go`、相关重启错误文案。
  - 测试：配置读取失败、API 超时、401、无效 JSON 均返回阻断错误；`--force` 放行。
- [x] P1-4 串行：阻止 loopback API DNS rebinding。
  - 修改：`api/server.go`、`api/auth.go`，新增请求边界校验 helper。
  - 测试：loopback Host 成功，外部 Host、跨源 Origin 拒绝，token 模式保持可用。
- [x] P2-1 串行：序列化 ACP 状态快照持久化。
  - 修改：`agent/acp_agent.go`、`agent/acp_state.go`。
  - 测试：并发保存最终文件可解析且不会由旧快照覆盖新状态。
- [x] P2-2 串行：修复 Companion 旧连接清理误伤新连接请求。
  - 修改：`agent/companion_agent.go`、`agent/companion_agent_chat.go`。
  - 测试：连接代际切换后旧 read loop 退出不失败新连接 pending request。
- [ ] P2-3 串行：为 ACP 启动增加 starting/ready 状态同步。
  - 修改：`agent/acp_agent.go`、`agent/acp_process.go`。
  - 测试：并发 `Start` 必须等待同一次 initialize 成功或共同收到失败。
- [ ] P2-4 串行：扩充远程请求特殊地址拒绝范围。
  - 修改：`internal/remotefetch/remotefetch.go`。
  - 测试：拒绝 CGNAT、benchmark、文档网段和 IPv6 特殊用途地址，保留合法公网地址。
- [ ] P2-5 串行：统一回收飞书临时附件。
  - 修改：`feishu/adapter_events.go`、`feishu/incoming.go` 或消息交付边界 helper。
  - 测试：多附件、空文本、处理中断和下载中途失败均清理临时文件。
- [ ] P2-6 串行：序列化并原子写入微信 context token。
  - 修改：`wechat/token_store.go`。
  - 测试：并发用户更新后文件包含全部最新 token，失败不破坏旧文件。
- [ ] P2-7 串行：回收长期状态表。
  - 修改：`messaging/handler.go`、`messaging/task_state.go`、`messaging/rate_limit.go`、`platform/registry.go`、`web/auth_throttle.go`、`feishu/adapter.go`。
  - 测试：过期键和空闲执行锁会被删除；删除未使用的 `contextTokens`。
- [ ] P2-8 串行：限制敏感日志并为后台日志增加轮转。
  - 修改：`api/send.go`、`messaging/audit.go`、`cmd/start_daemon.go`，必要时新增日志轮转 helper。
  - 测试：API 不记录正文，审计摘要脱敏，日志超过阈值后轮转。
- [ ] P2-9 串行：拒绝非法 HTTP Agent `max_history`。
  - 修改：`config/config.go`、`web/config_service.go`、`agent/http_agent.go`。
  - 测试：负值配置校验失败，构造函数不会产生可崩溃状态。
- [ ] P2-10 串行：加强稳定版发布门禁与 CI 最小权限。
  - 修改：`.github/workflows/release.yml`、`.github/workflows/ci.yml`。
  - 验证：YAML 结构检查、文档契约和发布脚本测试。
- [ ] P3-1 串行：完整判断 Web 配置是否需要重启。
  - 修改：`web/view.go`、`web/config_service.go`。
  - 测试：Agent、API、审计、保存目录等非热更新字段变化返回 `restart_required=true`。
- [ ] P3-2 串行：同步 Claude 模型与推理强度文档。
  - 修改：`README_CN.md`、`README.md`。
  - 验证：文档契约检查。
- [ ] FINAL 串行：执行全量测试、race、vet、staticcheck、govulncheck、覆盖率、文档契约和 Review Gate。

## 验证矩阵

- 每项先运行定向测试确认 RED，再实现并确认 GREEN。
- 逻辑改动完成后执行受影响包 `go test -race -count=1 -timeout 60s`。
- 阶段完成后执行 `go test ./... -count=1 -timeout 120s`。
- 终验执行 `go test -race ./...`、`go vet ./...`、`staticcheck ./...`、`govulncheck ./...`、覆盖率、文档校验与 `git diff --check`。

## Review 小结

终态：执行中。

## 进度记录

- 2026-07-11：P1-1 完成；Codex thread 与标准 ACP session 使用原子所有权注册，重复 owner 不再覆盖，注销仅清理调用者持有的通道；`go test -race ./agent` 通过。
- 2026-07-11：P1-2 完成；默认、命名和广播的非 Codex 执行统一登记 active task，Claude/HTTP 任务现已进入重启保护与状态统计；`go test -race ./messaging` 通过。
- 2026-07-11：P1-3 完成；已知服务进程存在时，配置损坏、API 不可达、未授权或响应损坏均阻断普通重启，仅显式 `--force` 放行；`go test -race ./cmd` 通过。
- 2026-07-11：P1-4 完成；无 token API 拒绝非 loopback Host 和跨源 Origin，显式 token 模式保持可用；`go test -race ./api` 通过。
- 2026-07-11：P2-1 完成；ACP 状态保存串行覆盖快照和写入，使用唯一 0600 临时文件原子替换，避免旧快照和固定 `.tmp` 互相覆盖；`go test -race ./agent` 通过。
- 2026-07-11：P2-2 完成；Companion pending call 原子绑定实际发送连接，旧连接替换或 read loop 退出只失败本代请求，不再清空新连接请求；`go test -race ./agent` 通过。
