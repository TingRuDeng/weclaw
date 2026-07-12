# 当前任务记录

## 目标

修复 v0.1.163 中飞书显式切换和新建 Codex 会话后仍无法对话的问题，同时保持普通消息不得隐式创建或接管会话的边界。

## 根因

- `createThread` 只更新 ACP thread map，没有把 owner registry 的 conversation 原子重绑到新 thread。
- `ClearCodexThread` 没有解除 owner registry 中的旧 conversation 绑定。
- `/cx new` 仍只写入 pending draft，依赖下一条普通消息隐式创建。
- 显式切换空闲的 `desktop_disconnected` thread 时，没有把用户操作视为接管授权。

## 已确认行为

- `/new` 和 `/cx new` 必须立即创建 thread，并绑定为 `weclaw_runtime`。
- 创建失败后不得继续保留旧 conversation owner 绑定。
- 用户显式切换到断线且 rollout 不活跃的 Desktop thread 时，允许 app-server 恢复同一 thread。
- rollout 活跃、普通消息自动准备会话、所有权未知时仍不得抢占或自动恢复。

## 任务清单

- [x] P1 串行 TDD：复现 ResetSession 创建新 thread 后仍指向旧 Desktop owner。
- [x] P2 串行实现：新增 owner registry 原子重绑和 conversation 解绑。
- [x] P3 串行 TDD：复现 `/cx new` 只创建 pending draft。
- [x] P4 串行实现：让 `/cx new` 立即调用 ResetSession 并记录新 thread。
- [x] P5 串行 TDD：复现显式切换空闲 disconnected thread 后仍无法恢复。
- [x] P6 串行实现：仅在显式切换且 rollout 不活跃时恢复 disconnected thread。
- [x] P7 串行验证：受影响测试、全仓测试、race、vet、staticcheck、文档和差异检查。
- [x] P8 串行审查：执行 review gate 并记录剩余风险。

## 验证命令

```bash
go test ./agent ./messaging -count=1 -timeout 60s
go test ./... -count=1 -timeout 120s
go test -race ./agent ./messaging -count=1 -timeout 120s
go vet ./...
staticcheck ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## 并行说明

不使用 subagent。owner registry、session store 与 runtime resolution 存在顺序依赖，串行修改和验证更容易证明状态转换正确。

## Review 小结

2026-07-12 修复飞书显式切换和新建 Codex 会话失败：新建 thread 后原子更新 ACP thread map、owner registry 和 route session store；清理会话时解除旧 conversation owner；`/cx new` 与 `/new` 都立即创建；显式切换可接管空闲 disconnected thread，但普通消息和活动 rollout 不得抢占。空工作空间和坏 thread 不再创建误导性 pending draft。

自动验收通过：`go test ./... -count=1 -timeout 120s`、`go test -race ./agent ./messaging -count=1 -timeout 120s`、`go vet ./...`、`staticcheck ./...`、文档校验和 `git diff --check` 均为退出码 0。Review gate 未发现阻塞性安全、状态一致性或静态检查问题；剩余风险是尚未用真实飞书按钮复测切换、新建和 disconnected 接管。
