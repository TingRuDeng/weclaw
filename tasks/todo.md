# 当前任务记录

## 目标

按安全审查确定的修复顺序，消除 ACP 权限绕过、依赖漏洞、协议漂移、工作区与任务状态机缺陷，以及平台可靠性、配置和更新流程问题。

## 执行任务

- [x] P1 串行：修复 ACP `sessionId` 路由与严格 fail-closed 权限选择。
- [x] P2 串行：升级 Go / `golang.org/x/net`，并把 `govulncheck` 加入发布门禁。
- [x] P3 串行：对齐 Codex / ACP 取消、完成状态、权限请求和实时进度协议。
- [x] P4 串行：修复工作区隔离、任务 owner 与 active task 生命周期。
- [x] P5 串行：修复飞书 / 微信可靠性、配置原子性、Web / API 和 update 语义。
- [x] P6 串行：运行全量验证、review gate 与逐项完成审计。

## 验证命令

```bash
go test ./... -count=1 -timeout 120s
go test -race ./... -count=1 -timeout 180s
go vet ./...
python3 scripts/validate_docs.py . --profile generic
govulncheck ./...
git diff --check
```

## 并行说明

本轮主流程串行执行。五个阶段存在协议、状态机和配置写入依赖，同一阶段的核心改动也集中在少量共享文件；只读分析和无写冲突验证可以并行，但生产代码统一由主流程整合。

## 进度记录

- 2026-07-10：完成安全审查和用户确认，开始按 P1 至 P6 顺序执行。
- 2026-07-10：P1 完成；标准 ACP 权限请求按 `sessionId` 路由，option kind 归一化并严格 fail-closed。`go test ./agent -count=1 -timeout 120s` 通过。
- 2026-07-10：P2 完成；Go 升级到 1.26.5，`x/net` 升级到 0.55.0，CI、Release workflow 和本地发布脚本加入 `govulncheck v1.6.0`。全量单测通过，漏洞扫描无发现。
- 2026-07-10：P3 完成；标准 ACP / Codex 远端取消、嵌套 turn 终态、permissions 审批、v2 item / patch 进度、Stop waiter 和 watcher 竞态均已补测试并修复。`go test ./agent -count=1 -timeout 120s` 通过。
- 2026-07-10：P4 完成；普通用户工作空间按白名单和服务端初始目录隔离，管理员显式绕过；Claude 使用会话级 cwd；群任务 owner 校验、暂存消息防覆盖、原子收尾和重启状态口径已修复。`go test ./messaging -count=1 -timeout 120s` 与任务状态定向 race 测试通过。
- 2026-07-10：P5 完成；修复飞书与微信消息消费、游标原子持久化和资源清理，统一配置原子保存与 `WECLAW_HOME`，补齐 Web/API 超时与前端输出转义，修复访问码并发、广播串行、更新失败传播及 Agent 凭据传递。受影响包单测与定向竞态测试通过。
- 2026-07-10：P6 完成；全量单测、全仓竞态检测、`go vet`、文档契约校验、`govulncheck` 与 `git diff --check` 全部通过；Review gate 未发现阻断项。

## Review 小结

终态：finished。

计划偏差：无。

Document-refresh: not-needed

原因：本轮公开权限、管理员工作区和 Web 配置语义已由现有中英文说明覆盖，`WECLAW_HOME` 仅统一内部状态根目录解析。

结论：通过。
