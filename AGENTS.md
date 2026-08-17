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
    - "cmd/start_runtime.go"
    - "codexauth/store.go"
    - "observability/store.go"
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
- `cmd/start.go` 负责启动命令、配置加载与预检；`cmd/start_runtime.go` 负责创建 `messaging.Handler`、Trace、HTTP API 与平台 registry，并管理排空和关闭顺序。
- `messaging/handler.go` 是命令路由、会话、审批、进度、任务状态和 Agent 调用的主要业务入口。
- `observability/` 提供固定字段、默认脱敏的端到端 Trace；本机 CLI/API 可按 message、task、thread、turn 或 stage 查询，Codex 协议正文只有显式启用后才会以脱敏形式记录。
- `agent/` 内包含 ACP、CLI、HTTP、Companion 等 Agent runtime；完整多前端共享只在已验证 official standalone daemon 上成立，它是唯一 Host authority，Codex App 只是连接该 daemon 的 frontend/history view。macOS `auto` 在固定 socket 上已有已验证 daemon 或 standalone 可用时会在构造期固定为 `daemon`，不因 App 可见而切换 Desktop authority；standalone 不可用时的 App 私有 Host 或 WeClaw-managed Host 只是兼容路径，不承诺完整共享。显式 `daemon` 可使用 Desktop IPC 协调，但不允许选择 App Host。活动 turn 输入直接 steer，未发送的 Desktop/CLI follow-up 仍是客户端草稿；Codex Companion 和旧 `codex exec` 第二 writer 已停用。
- shared managed Host、official daemon、受控 `weclaw codex cli` 与协调停止在启动、接管或变更 Host 前执行只读多 Host 预检：普通模式检查当前有效 UID，`run_as_user` 还检查目标 UID 及只用于 sudo wrapper 的 root UID；macOS/Linux 从系统接口读取候选进程的原始 argv，按 PGID 聚合 Node/native 进程，仅放行 metadata 或 lifecycle PID 证明的权威组。额外 Host、进程表或原始参数不可读、身份不确定时失败关闭；`--remote`、帮助、daemon/proxy/schema generation 等 tooling 不算 Host。错误只显示脱敏 PGID/PID，预检不停止进程，也不得按名称结束既有 App 或未知进程。该检查是时点门禁，不是持续全局锁；Desktop 私有 Host 兼容路径不因此获得受管身份。
- Codex 消息 route 使用 v14 两阶段 follower attach：`preparing` 只是持久化意图，普通写入失败关闭；精确 Host generation、历史回放、活动 turn 任务卡和 observer 全部就绪后才以 CAS 提交 `ready`。多 route 各自接收进度、交互展示和终态，审批/问答由 `(thread, turn, request)` broker 只提交一次。`/cx release` 先持久化 route tombstone，不停止 turn 或 Host；重启按 Host authority → history/interaction replay → observer readiness → terminal outbox 恢复。
- 原生 Codex `auto`/`daemon` 默认让后续启动的 macOS Codex App 通过受保护的 launchd 环境复用同一官方 daemon；设置前必须验证 daemon 与 App 推导出的 `CODEX_HOME` control socket 完全一致。已运行 App 若仍带私有 app-server，只能失败关闭并要求用户重启 App，禁止自动退出或按进程名清理；独立 `CODEX_SQLITE_HOME` 表示 App 接入后会看到 daemon 的目录，不得据此迁移或删除旧目录。
- `codexauth/` 管理 shared-host 级 Codex ChatGPT OAuth profile：系统凭据库优先、受保护文件显式降级；在线切换由 `agent/codex_account.go` 在 task/lease/thread 空闲门禁内停止和验证真实受管 Host，不能修改窗口 workspace/thread binding。
- `feishu/` 负责飞书事件、会话范围、卡片、按钮和审批；`wechat/` 与 `ilink/` 负责微信个人号接入。
- `scripts/release.sh` 和 CI 只为 GitHub 构建、上传 `darwin/arm64`、`linux/amd64` 正式资产及原始摘要，Gitee 镜像同两项资产的压缩表示和同一摘要。发布门禁包含安装脚本、文档、module tidy、全仓测试、race、vet、Staticcheck、govulncheck 和 `git diff --check`；本地发布通过 `WECLAW_GOCACHE`、调用方 `GOCACHE` 或平台默认值统一复用单一持久化 Go 缓存。
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

protocol-live (opt-in):

```bash
WECLAW_TEST_CODEX_DAEMON_PROTOCOL=1 \
WECLAW_TEST_CODEX_HOME=/path/to/prepared-isolated-codex-home \
go test ./agent -run '^TestCodexOfficialDaemonTwoClientProtocol$' -count=1 -timeout 300s -v
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
