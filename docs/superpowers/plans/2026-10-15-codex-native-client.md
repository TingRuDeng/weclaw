# Codex Native Client Implementation Plan

**Goal:** 将已批准的原生客户端方向落实为默认运行路径，减少对 App 和全局 Host 生命周期的接管。

**Architecture:** 标准 auto 配置解析为 native，复用官方 daemon 协议。显式旧模式继续兼容。原会话和消息可靠性状态不迁移。

**Tech Stack:** Go、官方 Codex App Server JSON-RPC/WebSocket、现有 Cobra CLI。

## Global Constraints

- 不新增前端独占规则，不删除或复制 Codex 数据。
- 架构实现阶段不操作日常 App/Host、不安装或发布；后续提交与发布按用户追加授权执行。
- 保留 pending 停止记录、未知交付和显式 force 的身份校验边界。

## Task 1：配置和原生连接

- [x] 在 `config/config.go`、`config/detect.go`、`agent/codex_daemon_host.go`、`agent/acp_constructor.go` 定义 native 与 auto 选择；在 `config/config_test.go`、新 `agent/codex_native_client_test.go` 中先验证默认策略及旧字段兼容。
- [x] 在 native 中跳过 App 环境/IPC 及全机 Host 预检，复用官方 daemon 身份验证和连接；显式旧模式保持原逻辑。
- [x] 在 `agent/codex_provider_runtime.go` 使用 native 官方恢复语义，测试不读写历史 SQLite。
- [x] 运行配置和 Agent 定向回归。

## Task 2：启动、CLI 与服务启停

- [x] 在 `cmd/start_agent.go`、`cmd/start_runtime.go` 退出默认 App 协调及同步预热。
- [x] 在 `agent/codex_cli_launch.go`、`cmd/codex_cli.go` 独立准备 native CLI，测试其不等待 WeClaw 服务或整个 CLI 会话租约。
- [x] 通过 `messaging.AgentMeta` 传递配置生命周期策略，普通 native 服务重启不为停止外部 Host 而启动 Agent。旧 journal 仍走真实恢复。
- [x] 在 `agent/codex_restart.go` 和 `cmd/restart.go` 补齐 native 普通重启及离线检查语义；显式 force 保持原路径。
- [x] 增加 App 启动覆盖的显式恢复入口，验证仅恢复备份中仍匹配的变量。
- [x] 运行 Agent、cmd、messaging 定向测试。

## Task 3：文档与最终验证

- [x] 同步 `README_CN.md`、`README.md`、`AGENTS.md`、`docs/AI_CONTEXT.md`、`docs/README.md` 与 `tasks/todo.md`，标注原生 App 活动任务接入能力边界。
- [x] 复核差异与旧模式兼容；全仓 test、race、vet、module tidy、Staticcheck、Linux amd64 交叉构建、文档与 diff 校验通过，最终相关回归通过。
- [x] 隔离官方 0.162.1 daemon 多客户端门禁通过（本地假模型）：恢复、审批回放与唯一决策、跨端 steer、unsubscribe、唯一终态及并发输入各提交一次；清理验证通过。真实 App/飞书/微信验收未执行。
- [x] 按 review-gate 复核需求、范围、数据安全和验证证据：代码阶段有条件通过；保留真实前端与部署验收边界，实现阶段未安装、未操作日常运行时、未提交或发布；后续发布按追加授权执行。
