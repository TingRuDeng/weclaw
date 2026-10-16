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
- 两阶段产物契约：`scripts/release_package.py`、`cmd/update_local.go`；`package` 验证和构建，`publish` 只上传相同提交和摘要的已有包，CI 按成功打包 run 恢复产物。

## Key facts

- 本仓库是 Go 单仓库，模块名在 `go.mod` 中声明为 `github.com/fastclaw-ai/weclaw`。
- WeClaw 把微信个人号和飞书消息接入 AI Agent；业务层尽量通过 `platform` 抽象隔离平台差异。
- `cmd/start.go` 负责启动命令、配置加载与预检；`cmd/start_runtime.go` 负责创建 `messaging.Handler`、Trace、HTTP API 与平台 registry，并管理排空和关闭顺序。
- `messaging/handler.go` 是命令路由、会话、审批、进度、任务状态和 Agent 调用的主要业务入口。
- `observability/` 提供固定字段、默认脱敏的端到端 Trace；本机 CLI/API 可按 message、task、thread、turn 或 stage 查询，Codex 协议正文只有显式启用后才会以脱敏形式记录。
- `agent/` 内包含 ACP、CLI、HTTP、Companion。标准 auto 默认 native，按需连接官方 standalone daemon；自定义 socket 或 run_as_user 的 auto 保留 managed。native 不修改 App 启动、不装配 Desktop IPC、不做全机额外 Host 排斥、不自动升级 CLI，不在普通选择时改写历史 provider。显式 shared、daemon、managed 保留旧实现。用户可从任意前端输入，无输入权转移门禁；同一执行端 active→steer、idle→start，独立 App stdio 的内存任务和专属工具不保证迁移。
- native 只验证目标官方 daemon 身份，不因其他 App/CLI Host 存在而阻断。显式旧模式在启动、接管或变更 Host 前保留只读多 Host 预检，按 PGID 聚合内核 executable 与原始 argv。显式 --force 继续核验实时 UID、PGID、启动时间、可执行文件和 argv 指纹后终止，不能仅凭名称或旧 PID。普通 native 服务启停只管理消息 admission 和本地任务，不要求关闭 App，不停止外部 daemon；旧 pending 记录必须真实恢复。
- Codex 消息 route 的 binding 只表示当前窗口选择，不再授予或排斥写入。v14 的 `preparing/ready` 继续记录 follower observer 同步状态并兼容旧版本回滚，但不参与普通消息授权；Host 与目标 thread 可读后立即提交 binding，历史回放、任务卡或 observer 失败只标记“进度同步已降级”。普通输入每次按权威 `thread/read` 决定 active→带 `expectedTurnId` 的 `turn/steer`、idle→`turn/start`，状态竞态重读后只提交同一输入一次；写后结果未知只用 turn/items 基线与消息摘要核对，无法证明时不自动重试、不排队、不保存完整输入。多 route 各自接收进度、交互展示和终态，审批/问答由 `(thread, turn, request)` broker 只提交一次。`/cx release` 先持久化 route tombstone，在没有其他观察者或活动任务时只执行 `thread/unsubscribe`，不停止 turn 或 Host；重启按 Host authority → history/interaction replay → observer readiness → terminal outbox 恢复。
- `weclaw codex app` 默认正常打开官方 App；显式 shared 才准备旧适配入口。app --restore 仅恢复保护备份中仍匹配应用值的启动变量，保留后改值，不退出 App。native CLI 不依赖服务 API 或 lifetime frontend lease。旧 shared 保留签名 Node、工具 pipe/MCP 刷新，显式 daemon 保留 local-daemon 检查；所有模式不删除历史数据库或锁。
- 旧服务没有协调接口时，`stop/restart/update --restart --force` 由新 CLI 接管：先记录 pending 并核验/停止旧服务，再持运行锁停止 Host；失败保留恢复记录及新版，后续 force 可重试，普通启动不得绕过 pending。
- `codexauth/` 管理 shared-host 级 Codex ChatGPT OAuth profile：系统凭据库优先、受保护文件显式降级；在线切换由 `agent/codex_account.go` 在 task/lease/thread 空闲门禁内停止和验证真实受管 Host，不能修改窗口 workspace/thread binding。
- `feishu/` 负责飞书事件、会话范围、卡片、按钮和审批；`wechat/` 与 `ilink/` 负责微信个人号接入。
- 全新配置不默认启用消息平台；未选择平台时 `doctor` 警告而 `start` 以 API-only 模式常驻。微信扫码只由显式 `weclaw wechat login` 触发；旧微信凭证在首次启动时迁移为显式启用，飞书已启用时不隐式同时启用微信。
- 持久配置缺少 `api_token` 时，`start` 与 `doctor --fix` 一次性生成并原子保存强随机 Token；普通配置读取和普通 `doctor` 只读，`WECLAW_API_TOKEN` 只做运行态覆盖且不得经配置更新写回。
- `scripts/release.sh` 和 CI 只为 GitHub 构建、上传 `darwin/arm64`、`linux/amd64` 正式资产及原始摘要，Gitee 镜像同两项资产的压缩表示和同一摘要。Gitee CI/Linux 认证优先使用外部 `GITEE_TOKEN`，macOS 本地缺失时回退读取 `weclaw-gitee-release` 登录钥匙串；API 只通过受保护临时请求头传递 Token，并在 Git 推送前验证目标仓库。发布门禁包含安装脚本、文档、module tidy、全仓测试、race、vet、Staticcheck、govulncheck 和 `git diff --check`；本地发布通过 `WECLAW_GOCACHE`、调用方 `GOCACHE` 或平台默认值统一复用单一持久化 Go 缓存。
- `tasks/todo.md` 只保留当前或正在执行的任务记录；已完成历史流水账不长期保留。
- `tasks/lessons.md` 是长期经验沉淀，清理文档时必须保留。
- 不要把机器本地绝对路径写入项目上下文文档；配置示例可以使用 `/path/to/project` 这类占位路径。
- 本机正式更新使用 `weclaw update`；发布前真机验证允许 `scripts/release.sh package --next-patch --install`，它通过新包中的 `update --from-package ... --target ...` 复用摘要校验、原子替换与预检失败回滚，不直接 cp、不自动重启。

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
scripts/release.sh package --next-patch
# 真机验证并推送相同提交后，上传已有包
scripts/release.sh publish vX.Y.Z
```

## Stale when

- 新增平台、Agent 类型、命令命名、配置字段或发布目标。
- `scripts/release.sh` 的验证命令或发布资产矩阵变化。
- `docs/README.md` 或 `docs/AI_CONTEXT.md` 的权威文档契约变化。
- 目录结构从单仓库变为 coordination root 或 monorepo。
