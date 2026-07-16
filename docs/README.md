---
ai_summary:
  purpose: "索引 WeClaw 当前权威上下文文档、任务记录、验证命令和文档维护边界。"
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
    - "新增权威文档、改变任务记录边界或改变发布/验证命令。"
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
| `docs/README.md` | 查找项目文档和验证命令时 | 索引权威文档、任务记录和验证命令 |
| `docs/AI_CONTEXT.md` | 理解模块、数据流、命令、测试和发布路径时 | 描述当前事实，必须有源码依据 |

## Task records

- `tasks/todo.md` 只记录当前或正在执行的任务，不长期累积已完成流水账。
- `tasks/lessons.md` 记录可复用的踩坑规则和高风险路径，清理文档时必须保留。
- `docs/CODE_REVIEW_2026-07-14.md` 是修复提交 `9af1731` 时点的历史复审快照；后续代码已继续演进，不能再作为当前缺陷状态来源。
- `docs/CODE_REVIEW_2026-07-13.md` 是 `9b42cda` 时点的历史深度审查快照；其架构与测试盲区分析可作背景参考，当前状态必须回到代码、测试和本索引核验。

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
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
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
- 任务记录边界、上下文包验证规则或 authority docs 范围变化。
