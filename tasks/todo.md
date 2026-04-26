# 微信进度摘要模式落地清单

## 目标

默认关闭微信里的实时正文 delta 流，改为“受理确认 + typing + 低频进度摘要 + 最终完整结果”。旧实时正文片段保留为 `progress.mode=stream` 兼容模式。

## 执行任务

- [x] 梳理现有配置、消息处理、ACP 路由和测试结构。
- [x] 新增 progress 配置默认值、全局配置、Agent 覆盖和环境变量覆盖测试。
- [x] 新增 progress 渲染、节流、去重和模式行为测试。
- [x] 新增 Handler summary / stream / Agent 覆盖 / MessageID 为 0 fallback 去重测试。
- [x] 新增 ACP 多 active turn 禁止任意 fallback 的测试。
- [x] 实现 `config.ProgressConfig`、默认值归一化、Agent 覆盖和 env override。
- [x] 接入 `cmd/start.go`，把全局和 Agent progress 配置传给 Handler。
- [x] 拆分 `messaging/progress.go`，实现 summary 默认模式、stream 兼容、受理确认、typing heartbeat、进度节流和错误文案。
- [x] 改造 `messaging/handler.go`，使用 progress 配置、文本 fallback 去重和最终回复稳定前缀。
- [x] 修复 `agent/acp_agent.go` 的多 active turn 任意 fallback 风险。
- [x] 更新 `README.md` 和 `README_CN.md` 的 progress 配置说明。
- [x] 运行受影响范围内测试和全量 Go 测试。
- [x] 补充 review 小结。

## Review 小结

已完成默认 `summary` 模式、`stream` 兼容模式、Agent 级覆盖、MessageID 为 0 的文本 TTL 去重、ACP 多 active turn fallback 限制和 README 配置说明。验证命令：`go test -count=1 ./...`，结果通过。当前仍未实现 typed event bus、FinalAssembler、运行时 `/progress` 切换、长结果分段和任务状态持久化，这些属于后续阶段范围。
