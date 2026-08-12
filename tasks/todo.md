# 当前任务记录

## 2026-08-12 基于最新代码同步项目文档

### 目标

以当前 `main` 的真实源码、命令帮助和运行日志为依据，审查全部受跟踪 Markdown 与上下文包，修正文档漂移并补齐用户可操作的边界说明。

### 范围

- 同步中英文 README 中 Codex App Host 会话绑定、运行通道不可用、`/status` 版本展示和恢复操作说明。
- 在维护者上下文与长期 lessons 中记录 Desktop `no-client-found` 的失败关闭语义，以及 `turn/start` 响应与进度、审批、终态事件可能乱序的生命周期屏障。
- 清理 `tasks/todo.md` 中已经与当前 Git/发布事实不一致的阶段描述，只保留仍需真机验收的边界。
- 逐一复核 `AGENTS.md`、`docs/README.md`、`docs/AI_CONTEXT.md`、三份设计基线、`THIRD_PARTY_NOTICES.md` 和文档校验器；准确内容不做机械改写，历史设计只补必要的状态或勘误说明。
- 不修改实现代码、配置、凭据、运行服务或发布状态。

### 验收标准

- [x] 中英文 README 对当前 Host 选择、会话绑定与故障恢复给出一致且可执行的说明，不把“绑定已提交”写成“运行通道已可用”。
- [x] `docs/AI_CONTEXT.md` 与 `tasks/lessons.md` 准确描述 Desktop handler 缺失和 `turn/start` 事件乱序边界，并保持上下文文档长度契约。
- [x] 当前任务记录不再声称已提交代码“尚未提交、推送或发布”，真实飞书/App/CLI 端侧验收仍明确标为未完成。
- [x] 所有权威文档路径、发布资产矩阵、权限模型、命令列表和验证命令与当前源码一致；无本机绝对路径或敏感信息进入文档。
- [x] 文档编译、上下文校验和差异检查通过。

### 实施步骤

- [x] 只读核对工作树、最近提交、CLI 帮助、全部 Markdown、上下文校验器与当前运行故障证据。
- [x] 更新 README_CN.md、README.md 的用户说明并保持双语语义一致。
- [x] 更新维护者上下文、长期 lessons 和必要的设计基线勘误。
- [x] 收敛当前任务记录，复核全部文档最终差异。
- [x] 运行文档验证并记录 Review。

### 验证方式

```bash
python3 -m py_compile scripts/validate_docs.py
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### Review

- 已按 upgrade 模式审查当前上下文包与全部受跟踪 Markdown；只修改 7 个计划内文档文件，没有改动实现、配置、凭据、服务或发布状态。
- 中英文 README、维护者上下文、长期 lessons 与单 Host 设计基线已经统一会话 binding/runtime 边界、Desktop `no-client-found` 恢复方式、`turn/start` 事件乱序屏障、v9 持久化状态和 `/status` 版本展示。
- `python3 -m py_compile scripts/validate_docs.py`、`PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic` 与 `git diff --check` 均以退出状态 0 完成；`docs/AI_CONTEXT.md` 保持 120 行，最终差异仅包含计划内文档。
- 本轮为纯文档同步，未运行 Go 测试；真实 Codex App、受控 CLI、飞书桌面端/移动端和进程异常退出仍保留为下方端侧验收项，未把自动化结果写成真机结论。

## 2026-08-09 Codex 协作与飞书任务卡端侧验收

### 目标

在真实 Codex App、受控 CLI、飞书桌面端和移动端验证同一 Host/thread 协作、会话绑定恢复、引导接力卡和进度折叠。自动化与正式发布不能替代端侧验收。

### 待验收

- [ ] App 是当前 Host 且目标 thread 已在 App 打开时，从飞书选择同一会话，确认运行通道可用并能持续同步。
- [ ] App 存在但目标会话没有可用 Desktop handler 时，确认飞书显示“已选择，等待运行通道”、普通消息被阻止；在 App 打开准确会话并重新选择后恢复。
- [ ] 官方 daemon 是当前 Host 时，用 `weclaw codex cli` 打开同一 thread，确认 CLI 与飞书看到同一任务状态并可继续输入。
- [ ] 从飞书发送普通消息或 `/guide`，确认输入进入当前 active turn，不创建第二个 writer 或私有续跑任务。
- [ ] 执行 `/cx release`，确认本地任务继续运行，原飞书窗口不再收到进度、审批、问答或最终结果；重新绑定后从最新快照继续。
- [ ] 连续发送多条成功引导，确认每条消息下各生成一张新接力卡，旧卡显示“已转移”并停止更新。
- [ ] 确认活动卡可手动折叠且普通更新不重置展开状态；完成、失败、停止和已转移卡自动折叠。
- [ ] 在飞书桌面端与移动端分别确认最新卡持续接收进度和唯一终态，最终结果消息不重复。
- [ ] 做一次真实进程异常退出恢复演练，确认 durable binding、活动卡恢复项和 observer 重挂不会把仍在运行的共享 turn 误报为停止。

### 已完成边界

- [x] 同 Host/thread 协作、活动 turn steer、durable follower 重挂、`/cx release` 非中断解除和全局 `/stop` 已实现。
- [x] 账号级 `allowed_users` capability、机器人隔离和旧 `admin_users` 忽略告警已实现。
- [x] 引导接力卡、折叠面板、终态收敛、重启恢复和两路独立投递已通过自动化验证。
- [x] 权威发布门禁已验证安装脚本、全仓测试、Race、Vet、Staticcheck、govulncheck、文档、module tidy 和差异检查。
- [x] 相关实现已合入 `main`，`v0.1.266` 指向当前提交 `fa8d16d`；本机正式二进制已更新到该版本。
