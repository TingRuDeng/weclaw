---
ai_summary:
  purpose: "为维护者和自动化编码代理提供 WeClaw 仓库的入口规则、文档路由和验证边界。"
  read_when:
    - "开始修改 WeClaw 代码、测试、发布流程或上下文文档前。"
    - "需要判断应该读取哪些项目文档或运行哪些验证命令时。"
  source_of_truth:
    - "README_CN.md"
    - "docs/README.md"
    - "docs/AI_CONTEXT.md"
    - "tasks/lessons.md"
    - "tasks/todo.md"
    - "scripts/release.sh"
    - "codexauth/store.go"
    - "go.mod"
  verify_with:
    - "python3 scripts/validate_docs.py . --profile generic"
    - "git diff --check"
  stale_when:
    - "新增或删除顶层模块、命令入口、平台 adapter、发布脚本或上下文文档结构。"
    - "验证命令、发布目标或默认开发流程发生变化。"
---

# WeClaw 代理上下文

## Purpose

本文件是 WeClaw 仓库的可移植代理入口。它只负责路由和约束；项目事实、模块地图和验证细节以 `docs/README.md` 与 `docs/AI_CONTEXT.md` 为准。

## Source of truth

- 产品与使用说明：`README_CN.md`、`README.md`
- 上下文索引：`docs/README.md`
- 代码地图：`docs/AI_CONTEXT.md`
- CLI 和服务入口：`cmd/`
- 跨平台消息业务：`messaging/`
- Agent 接入：`agent/`
- Codex OAuth 账户存储：`codexauth/`
- 平台 adapter：`wechat/`、`feishu/`、`platform/`
- 配置结构：`config/config.go`
- 发布脚本：`scripts/release.sh`

## Key facts

- 本仓库是 Go 单仓库，模块名在 `go.mod` 中声明为 `github.com/fastclaw-ai/weclaw`。
- WeClaw 把微信个人号和飞书消息接入 AI Agent；业务层尽量通过 `platform` 抽象隔离平台差异。
- `cmd/start.go` 负责加载配置、创建 `messaging.Handler`、启动 HTTP API 与平台 registry。
- `messaging/handler.go` 是命令路由、会话、审批、进度、任务状态和 Agent 调用的主要业务入口。
- `agent/` 内包含 ACP、CLI、HTTP、Companion 等 Agent runtime；Codex 生产路径使用稳定 Unix socket 上的单一共享 app-server，客户端按上游 WebSocket-over-UDS 协议连接，窗口只保存 frontend binding，Codex Companion 第二 writer 已停用。
- `codexauth/` 管理 shared-host 级 Codex ChatGPT OAuth profile：系统凭据库优先、受保护文件显式降级；在线切换由 `agent/codex_account.go` 在 task/lease/thread 空闲门禁内停止和验证真实受管 Host，不能修改窗口 workspace/thread binding。
- `feishu/` 负责飞书事件、会话范围、卡片、按钮和审批；`wechat/` 与 `ilink/` 负责微信个人号接入。
- `scripts/release.sh` 构建 `darwin/arm64`、`darwin/amd64`、`linux/arm64`、`linux/amd64` 四个正式资产，并会运行测试、race、vet 和 `git diff --check`；本地发布通过 `WECLAW_GOCACHE`、调用方 `GOCACHE` 或平台默认值统一复用单一持久化 Go 缓存。
- `tasks/todo.md` 只保留当前或正在执行的任务记录；已完成历史流水账不长期保留。
- `tasks/lessons.md` 是长期经验沉淀，清理文档时必须保留。
- 不要把机器本地绝对路径写入项目上下文文档；配置示例可以使用 `/path/to/project` 这类占位路径。
- 发布后本机安装必须走 `weclaw update`，不要用本地构建产物直接覆盖 PATH 中的 `weclaw`。

## How to verify

quick:

```bash
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

full:

```bash
go test ./... -count=1 -timeout 120s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
```

release-side-effect:

```bash
scripts/release.sh --next-patch
```

## Stale when

- 新增平台、Agent 类型、命令命名、配置字段或发布目标。
- `scripts/release.sh` 的验证命令或发布资产矩阵变化。
- `docs/README.md` 或 `docs/AI_CONTEXT.md` 的权威文档契约变化。
- 目录结构从单仓库变为 coordination root 或 monorepo。
