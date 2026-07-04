# WeClaw 单实例运行治理

## 已确认计划

- [x] T1 串行：补启动治理回归测试，覆盖 runtime lock、JSON 运行状态、doctor 进程诊断。
- [x] T2 串行：实现 `start` / `start -f` 单实例锁与运行状态写入。
- [x] T3 串行：增强 `status` / `stop` / `restart` 对 JSON 状态的兼容读取。
- [x] T4 串行：新增 `doctor processes` 诊断残留进程和多安装路径。
- [x] T5 串行：运行目标测试、全量测试和交付前审查。

## 执行记录

- 2026-07-03：用户确认按“单实例运行模型”推进。
- 2026-07-03：已完成 runtime lock、JSON 运行状态、`doctor processes` 和 update 路径校验；目标测试 `go test ./cmd -count=1 -timeout 60s` 通过。
- 2026-07-03：全量验证通过：`go test ./... -count=1 -timeout 120s`、`go vet ./...`、`git diff --check`。

## Review 小结

- Spec 符合度：通过；已覆盖单实例锁、运行状态、进程诊断和 update 路径校验。
- 安全检查：通过；没有新增 secret，没有静默清理或误杀进程。
- 测试与验证：通过；新增 cmd 包回归测试，并完成全量测试、vet、diff check。
- Document-refresh: not-needed
  原因：本轮是运行治理实现，已用任务文件记录执行状态，未改公开使用文档。
