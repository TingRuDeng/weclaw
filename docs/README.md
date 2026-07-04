---
ai_summary:
  purpose: "索引 WeClaw 当前权威上下文文档、旧细节记录、验证命令和文档维护边界。"
  read_when:
    - "需要快速判断某类任务应该读取哪些项目文档时。"
    - "更新上下文文档、发布说明或验证策略前。"
  source_of_truth:
    - "AGENTS.md"
    - "docs/AI_CONTEXT.md"
    - "README_CN.md"
    - "scripts/release.sh"
    - "tasks/lessons.md"
  verify_with:
    - "python3 scripts/validate_docs.py . --profile generic"
    - "python3 -m py_compile scripts/validate_docs.py"
  stale_when:
    - "新增权威文档、废弃旧细节文档或改变发布/验证命令。"
    - "README_CN.md、tasks/lessons.md 或主要源码目录描述发生变化。"
---

# WeClaw 文档索引

## Purpose

本索引说明 WeClaw 的当前上下文包如何使用。新任务优先读取本页，再按任务类型跳到 `docs/AI_CONTEXT.md`、`README_CN.md` 或具体源码目录。

## Source of truth

- 代理入口与协作规则：`AGENTS.md`
- 当前代码地图和模块事实：`docs/AI_CONTEXT.md`
- 用户使用和配置说明：`README_CN.md`、`README.md`
- 发布、打包、校验逻辑：`scripts/release.sh`、`cmd/update.go`
- 历史经验和高风险规则：`tasks/lessons.md`
- 进行中的任务状态：`tasks/todo.md`

## Key facts

- 当前上下文包语言为简体中文，文件名、命令、包名和源码路径保持原样。
- `AGENTS.md` 是轻量路由入口，不承载完整架构说明。
- `docs/AI_CONTEXT.md` 是当前权威代码地图；修改模块边界、命令入口或发布流程后必须同步更新。
- `README_CN.md` 与 `README.md` 是产品级用户文档，不替代面向维护者的上下文包。
- `tasks/lessons.md` 记录已踩坑规则，涉及发布、重启、飞书审批、Codex 会话等高风险路径时必须先读取。
- `tasks/todo.md` 是阶段性执行记录，不作为长期稳定架构事实。
- `scripts/validate_docs.py` 是上下文包验证入口，更新本目录文档后必须运行。

## Current authority docs

| 文档 | 何时读取 | 维护边界 |
| --- | --- | --- |
| `AGENTS.md` | 开始任何代码、测试、发布或上下文文档任务前 | 只放路由、约束、验证入口 |
| `docs/README.md` | 查找项目文档和验证命令时 | 索引权威文档与 legacy detail docs |
| `docs/AI_CONTEXT.md` | 理解模块、数据流、命令、测试和发布路径时 | 描述当前事实，必须有源码依据 |

## Legacy detail docs

这些文件保留为历史 PR 细节记录。它们不是当前完整权威文档；使用前需要对照源码和当前上下文包复核。

- `docs/pr-a-feishu-session-scope.md`：飞书会话范围收口记录；相关源码以 `feishu/session_scope.go`、`feishu/incoming.go`、`messaging/feishu_route.go` 为准。
- `docs/pr-b-feishu-approval-card.md`：飞书审批卡片收口记录；相关源码以 `feishu/choice.go`、`feishu/approval_panel.go`、`messaging/handler.go` 为准。

## How to verify

quick:

```bash
python3 -m py_compile scripts/validate_docs.py
python3 scripts/validate_docs.py . --profile generic
```

full:

```bash
go test ./... -count=1 -timeout 120s
go vet ./...
git diff --check
```

release-side-effect:

```bash
scripts/release.sh --next-patch
```

## Stale when

- `cmd/` 新增或移除用户命令。
- `agent/` 新增 Agent 类型或改变 Codex / Claude 会话控制路径。
- `platform/`、`wechat/`、`feishu/` 的消息模型或能力降级策略变化。
- `scripts/release.sh`、`cmd/update.go` 或发布资产矩阵变化。
- legacy detail docs 被迁移、删除或升级为当前 authority docs。
