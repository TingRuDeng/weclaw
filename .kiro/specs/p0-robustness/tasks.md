# Implementation Plan — P0 健壮性与隔离

## Overview

落实代码审查后提出的 P0 两项（借鉴 cc-connect）：
- **P0-1 单轮 wall-clock 上限 + 子进程树优雅强杀 + 自动续接**：防止一条卡死的 bash/测试命令锁死会话。
- **P0-2 `run_as_user` OS 级隔离 + `weclaw doctor` 预检**：缩小"bot 驱动 shell agent"的暴露面，并提供启动前体检。

约束：不破坏现有平台抽象、微信/飞书行为；每步以 `go build/vet/test ./...`（含 `-race`）全绿为门槛。`run_as_user`（sudo 隔离）涉及真实环境，无法在沙箱完整验证，按"代码+文档+doctor 门控"交付并显式标注验证边界。

## Tasks

### P0-1 单轮超时强杀 + 续接

- [ ] 1. 配置：新增单轮 wall-clock 上限
  - `config.ProgressConfig` 已有 `TaskTimeoutSeconds`（仅 cancel context）。复用它作为"单轮上限"，无需新字段；在文档说明其语义升级为"软超时→强杀子进程树"。
  - _Requirements: P0-1_

- [ ] 2. CLI agent 子进程置于独立进程组并优雅强杀
  - `agent/cli_agent.go`：spawn 时设置 `SysProcAttr{Setpgid:true}`（unix），ctx 取消/超时时对整个进程组先 SIGTERM、宽限期后 SIGKILL，避免 claude/codex 派生的 bash 子进程成为孤儿
  - 平台分文件：`agent/proc_kill_unix.go` / `agent/proc_kill_windows.go`（windows 退化为 `Process.Kill`）
  - _Requirements: P0-1_

- [ ] 3. 超时回复与自动续接
  - 超时被强杀时，向用户回"上一轮已超时中止，可继续/`/new`"；claude CLI 依赖已持久化 session 下一条自动 `--resume` 续接；codex exec 无状态，提示重发
  - _Requirements: P0-1_

- [ ] 4. P0-1 测试
  - 单测：注入一个会挂起的假命令 + 短超时，断言进程组被回收、`Chat` 在上限内返回错误、不留孤儿
  - _Requirements: P0-1_

### P0-2 run_as_user 隔离 + doctor

- [ ] 5. 配置：新增 `run_as_user` / `run_as_env`
  - `config.AgentConfig` 增加 `RunAsUser string`、`RunAsEnv []string`
  - _Requirements: P0-2_

- [ ] 6. CLI/ACP spawn-as-user 管线
  - 当 `run_as_user` 非空且非当前用户时，通过 `sudo -n -u <user>` 包装命令；按 `run_as_env` 白名单透传环境变量；非 unix 或留空时保持现状
  - 显式标注：sudo 隔离需真实环境验证
  - _Requirements: P0-2_

- [ ] 7. `weclaw doctor` 预检命令
  - 新增 `cmd/doctor.go`：检查 config 可解析、各 agent 二进制可达（复用 `lookPath`）、平台凭证存在、API token 在非 loopback 时必填；若配了 `run_as_user` 做越权探测（目标用户能否读写 work_dir、是否有跨用户泄露），任一硬门失败则 doctor 报错
  - _Requirements: P0-2_

- [ ] 8. P0-2 测试
  - doctor 各检查项的单测（注入桩）；run_as_user 命令包装的单测（断言 argv 构造，不实际 sudo）
  - _Requirements: P0-2_

- [ ] 9. 全量回归
  - `go build/vet/test ./...` + `-race` 全绿；微信/飞书零回归
  - _Requirements: P0-1, P0-2_

## Notes

- 优先级：先 P0-1（纯健壮性、可完整测试、收益直接），再 P0-2。
- `run_as_user` 默认关闭，零配置用户无感；sudo 路径在沙箱只验证 argv 构造与 doctor 门控，真实隔离需用户在目标机验证。
- 平台：当前开发机为 darwin，进程组/信号可用；windows 走退化分支。
