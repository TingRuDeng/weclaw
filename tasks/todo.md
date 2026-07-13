# 当前任务记录

## 目标

让 WeClaw 安装脚本在已安装 Claude CLI 时自动补齐 `claude-agent-acp`，并让更新、重启在停止现有服务前完成 Claude ACP 依赖预检。

完整 Spec 与执行计划：`tasks/claude-acp-installation.md`。

## 任务清单

- [x] P0 串行：确认需求、失败语义、版本策略和 HARD-GATE。
- [x] P0 串行：建立隔离工作树并完成全仓基线测试。
- [x] P1 并行：实现安装脚本自动安装、跳过开关和 Shell 测试。
- [x] P1 并行：实现 Claude ACP 安装校验及更新、重启预检。
- [x] P2 串行：整合实现并同步中英文使用说明。
- [x] P3 并行：执行 Shell、Go、Race、Vet、Staticcheck 和文档验证。
- [x] P4 串行：执行 `review-gate` 并记录 Review 小结。

## 当前状态

P0 至 P4 已完成，准备提交。

## Review 小结

Review Gate 终态为 `finished`，结论通过。安装脚本覆盖无 Claude、显式跳过、已有适配器、默认与覆盖版本、非法版本、npm 缺失、npm 失败、能力配置失败和发布门禁共 10 个隔离用例；测试不会修改真实 npm 全局环境。更新与重启在停止旧服务前完成同一配置快照的命令解析和 ACP 能力握手，失败时不调用停止。新增核心编排函数覆盖率为 83.3% 至 100%；全仓测试、Race、Vet、Staticcheck、构建、文档校验和差异检查全部通过。剩余风险是 npm 全局目录权限和不同 Node 版本管理器的真实机器差异，失败路径会保留 WeClaw 并返回明确修复命令。
