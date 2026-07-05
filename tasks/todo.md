# 当前任务记录

## 目标

落实核心文件拆分：对 `messaging/handler.go` 与 `agent/acp_agent.go` 做零行为变更拆分，把审批、任务状态、Codex 会话命令、Agent 执行主流程、ACP/Codex 协议、运行时、thread/session 与审批桥接移到同包独立文件，降低后续飞书/Codex 回归定位成本。

## 执行任务

- [x] 串行：确认工作区干净并读取当前执行约束。
- [x] 串行：P0 机械拆分审批 pending、审批按钮、审批文案与选项解析。
- [x] 串行：P1 机械拆分 active task、pending guide、pending Codex run 与任务命令。
- [x] 串行：P2 继续拆小新文件，并机械拆分 Codex 会话命令、本地入口、切换与状态渲染。
- [x] 串行：P3 机械拆分 Agent 执行主流程、Codex 后台任务与回复投递。
- [x] 串行：P4 运行最小充分验证与交付前 review-gate。
- [x] 串行：P5 继续零行为拆分入站附件处理逻辑。
- [x] 串行：P6 继续零行为拆分内置命令、平台路由、状态/help/progress/cwd helper。
- [x] 串行：P7 继续零行为拆分 Agent 会话解析、默认会话重置、配置 setter、去重和构造器。
- [x] 串行：P8 机械拆分 ACP/Codex 协议类型、构造器与进程生命周期。
- [x] 串行：P9 机械拆分 ACP session/thread 管理、状态持久化与 JSON-RPC 基础设施。
- [x] 串行：P10 机械拆分 Codex app-server turn、事件处理、错误格式化与审批桥接。
- [x] 串行：P11 运行全量验证与交付前 review-gate。

## Review 小结

已完成核心文件零行为拆分：`messaging/handler.go` 中的审批 pending、审批按钮、审批文案、审批选项解析、yolo 模式、active task、pending guide、pending Codex run、`/run`、`/guide`、`/stop`、`/ps`、Codex 会话命令分发、本地入口、thread 切换、状态渲染、Agent 执行、Codex 后台任务、广播执行、回复投递、入站附件、内置命令、平台路由、状态/help/progress/cwd helper、Agent 会话解析、默认会话重置、配置 setter、消息去重和构造器已移动到同包独立文件；`agent/acp_agent.go` 中的 ACP/Codex 协议类型、构造器、进程生命周期、session/thread 管理、状态持久化、JSON-RPC 基础设施、Codex app-server turn、事件处理、错误格式化与审批桥接已移动到同包独立文件。`messaging/handler.go` 从 3602 行降到 286 行，`agent/acp_agent.go` 从 2465 行降到 76 行；本轮新增拆分文件均低于 300 行。本轮未改变函数签名、调用点和业务分支。验证命令：`GOCACHE=/private/tmp/weclaw-go-cache go test ./agent -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`python3 -m py_compile scripts/validate_docs.py`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check`，结果均通过；`py_compile` 生成的 `scripts/__pycache__/validate_docs.cpython-310.pyc` 已清理。
