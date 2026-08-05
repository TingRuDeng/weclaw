# 当前任务记录

## 2026-08-05 Codex provider 会话切换脚本

### 目标

提供一个可重复、可回滚的本机脚本，在 Codex 内置 provider `openai` 与中转站 provider `OpenAI` 之间切换现有会话的归属元数据，并针对已确认的 Responses item ID 类型前缀不兼容提供显式修复能力。

### 范围与非目标

- 新增 `scripts/codex-provider-switch.sh`，只接受大小写精确的 `openai` 或 `OpenAI`，默认只预览，必须显式 `--apply` 才写入。
- 同步修改 `state_5.sqlite.threads.model_provider`、当前与归档 rollout 中的全部 `session_meta.payload.model_provider`，并在 Desktop 本地 thread catalog 存在记录时同步其 provider 缓存。
- 写入前确认 Codex App、app-server 和相关 SQLite/rollout 没有活动 writer，创建带清单和校验结果的独立备份；不自动停止进程、不自动重启 WeClaw 或 Codex App。
- 提供显式 Responses item ID 修复：按条目类型把错误的通用 `item_` 前缀改为 Codex/OpenAI 约定的类型前缀，并保持同一 rollout 内引用一致；不伪造 API 响应、不删除会话正文。
- 不承诺任意 OpenAI-compatible provider 的完整线协议都能跨端点互通；`encrypted_content`、provider 专有 item 类型和工具能力差异仍作为预检告警与剩余风险保留。
- 不修改 `config.toml`、OAuth/API 凭据、模型配置、工作空间绑定、thread ID、会话标题或 Codex App 项目分组。

### 已确认根因

- Codex 的会话发现按 `model_provider` 精确过滤，因此 `OpenAI` 与 `openai` 会形成两个可见性桶；底层 rollout 和 SQLite 数据并未删除。
- 当前失败会话中的 `input[256]` 对应 rollout 内 `type=function_call,id=item_637374b52cfaa643df8fee1e`，而该类型要求 `fc_` 前缀。
- Codex 会把持久化的 `ResponseItem` 重新放入 Responses API `input`；上游现有兼容处理只删除完全无前缀 ID，`item_...` 因包含下划线而被保留。
- 本机当前 25 个会话中有 7 个 rollout、63 个 response item 的 ID 前缀与条目类型不匹配；23 个会话含加密 reasoning，说明 ID 修复不能被表述为通用跨 provider 兼容保证。

### 验收标准

- dry-run 能准确报告目标 provider、SQLite 行数、rollout/session_meta 数量、catalog 缓存数量、ID 修复数量和兼容风险，且不改文件。
- `--apply` 在存在 writer、目标结构不符合预期、JSON 无效、磁盘空间不足、备份或校验失败时保持失败语义并拒绝写入。
- 成功切换后，当前与归档会话的全部 provider 元数据一致，SQLite integrity check 通过，rollout 仍逐行是合法 JSON。
- ID 修复只处理已知 response item 类型的错误前缀，目标 ID 无碰撞，同一 rollout 内相关引用保持一致；再次执行结果幂等。
- 自动化 fixture 覆盖 `OpenAI -> openai`、`openai -> OpenAI`、dry-run、归档会话、多条 `session_meta`、错误 `item_` 前缀、catalog 缓存、重复执行和恢复备份。

### 实施步骤

- [ ] 先补临时 Codex home/SQLite/rollout fixture，锁定 dry-run、双向 provider 切换、ID 修复和恢复行为。
- [ ] 实现输入校验、只读盘点、writer 门禁、空间检查、备份清单和 staged 原子替换。
- [ ] 实现 SQLite、全部 `session_meta` 与可选 catalog 同步，以及类型特定的 item ID 修复与碰撞校验。
- [ ] 实现备份恢复入口和幂等校验，不自动操作 Codex/WeClaw 进程。
- [ ] 执行 shell 语法/静态检查、fixture 测试、文档校验和 diff 复核。

### 验证方式

- `bash -n scripts/codex-provider-switch.sh scripts/codex_provider_switch_test.sh`。
- `bash scripts/codex_provider_switch_test.sh`。
- 若本机存在 ShellCheck，执行 `shellcheck scripts/codex-provider-switch.sh scripts/codex_provider_switch_test.sh`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。
- 只对备份副本执行真实数据 dry-run；未经单独确认不关闭当前 Codex App、不改写真实 `~/.codex`。

### 回滚与修正策略

每次写入前对 SQLite 使用一致性备份，对所有将修改的 rollout 和可选 catalog 建立独立备份与清单。恢复入口只接受脚本生成且校验通过的备份目录；恢复时同样要求无 writer，并通过 staged 替换和 SQLite integrity check 完成。任何中途失败保留备份和错误日志，不自动重试、不自动启动服务。

## 2026-08-05 飞书凭证权限自动修复

### 目标

在启动和更新后的重启预检中，自动收紧当前用户拥有的飞书凭证目录与文件权限，避免历史目录权限漂移导致服务无法启动。

### 范围与安全边界

- 目录仅允许从可验证的真实目录收紧到 `0700`，文件仅允许从当前用户拥有的真实普通文件收紧到 `0600`。
- 目录属主异常、符号链接、非目录或非普通文件继续 fail-closed，不自动接管或覆盖。
- 更新流程复用启动凭证加载路径，不新增独立的凭证迁移或凭证内容修改行为。

### 验收标准

- 现有 `0755`/`0775` 等当前用户目录可在读取凭证前收紧到 `0700`。
- 现有凭证文件权限过宽时可收紧到 `0600`，文件内容保持不变。
- 缺失凭证仍返回原有登录提示；符号链接和属主异常仍被拒绝。
- 相关单元测试、全仓测试、race、vet、staticcheck、文档校验和 diff 检查通过。

### 实施步骤

- [x] 先补权限自动修复和 fail-closed 回归测试。
- [x] 实现 securefile 权限修复并接入飞书凭证读取路径。
- [x] 执行受影响测试和全仓验证，复核最终 diff。

### 回滚与修正策略

若自动修复边界或启动行为出现回归，只回退凭证读取路径和 securefile 修复 API；不放宽现有属主、符号链接、目录类型和凭证文件安全校验。

### Review 小结（2026-08-05）

- `securefile.RepairPermissions` 只对当前用户拥有的真实目录和普通文件收紧权限；目录变为 `0700`、文件变为 `0600`，不改凭证内容。
- 飞书凭证读取路径在最终 `securefile.Read` 校验前执行安全修复；缺失文件、符号链接、属主异常和类型异常仍保持原有失败语义。
- `go test ./feishu ./internal/securefile ./cmd`、`go test ./...`、核心包 race、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验和 `git diff --check` 均通过。

## 2026-08-05 最新代码审查问题修复

### 目标

修复提交 `967f3ee` 上重新审查确认的 Codex runtime gate、iLink 错误退避、无消息 ID 去重、高权限审计、临时附件、错误脱敏、微信未授权状态、每用户限流、二维码登录退避和 Codex 设计文档漂移问题。

### 范围与非目标

- 范围：`agent/`、`codexauth/`、`ilink/`、`messaging/`、`platform/`、`wechat/`、`feishu/` 及对应测试和 Codex 详细设计文档。
- 不修改 Agent 公共协议、会话 binding、writer lease、平台白名单和管理员授权语义。
- 不处理未能复现的 iLink 孤儿队列推论，不为当前不可达的 `GO-2026-5942` 单独升级依赖。
- 不发布、不重启本机服务、不切换 Codex 账号，也不运行会影响真实 Codex App/daemon 的烟测。

### 验收标准

- 尚未启用多账号索引时，现有非 `0700` `CODEX_HOME` 不再误伤 Codex Host；索引存在时仍严格校验目录、owner、文件和切换 journal，并保留可诊断失败原因。
- iLink 任一业务错误码非零都进入有界退避，不提前重置失败计数或刷新成功活动时间；二维码轮询的即时传输错误也有可取消退避。
- 无稳定 MessageID 的相同文本在首条消息完成任务准入前并发重投时只允许一个 reservation，任务结束后仍允许用户主动重发相同文本。
- `/update`、`/restart`、远程飞书身份授权/撤销、卡片或文本审批及停止动作留下不含凭据和授权短码的结构化审计记录。
- 飞书附件在已验证为当前用户持有的 `0700` 目录内，以 `0600` 排他临时文件承接 SDK 写入；普通 Agent 错误日志统一脱敏并限长。
- 微信 context token 只为已授权身份持久化；授权码状态有明确容量上限；限流键按真实平台账号和操作者计算，不再按飞书群 route 共享。
- Codex 详细设计文档与当前 Desktop IPC / daemon / managed Host 三路径和单一写入权威一致。

### 实施步骤

- [x] 先补回归测试，覆盖 runtime gate、业务错误、去重 reservation、审计、临时目录、脱敏、授权后 token、容量、限流和二维码退避。
- [x] 修复 Codex 账号索引探测顺序，并让 gate 保存和返回失败原因。
- [x] 修复无 MessageID 去重 reservation、真实操作者限流和高权限审计。
- [x] 修复 iLink monitor 与二维码登录的业务错误判断和可取消退避。
- [x] 加固飞书临时附件、Agent 错误脱敏、微信 token 持久化时机和授权码容量。
- [x] 同步 Codex 详细设计文档及文档索引语义。
- [x] 执行定向测试、全仓测试、核心 race、vet、模块差异、文档校验、格式和 diff 复核。
- [x] staticcheck：`/Users/dengtingru/go/bin/staticcheck ./...` 通过，无任何告警；版本为 `2026.1 (v0.7.0)`。
- [x] govulncheck：`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...` 通过；调用路径受影响漏洞为 0。

### 验证方式

- `go test` 定向覆盖 `./agent ./ilink ./messaging ./platform ./wechat ./feishu`。
- `go test ./... -count=1 -timeout 180s`。
- `go test -race ./agent ./messaging ./feishu ./ilink ./platform ./wechat -count=1 -timeout 240s`。
- `go vet ./...`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`、`go mod tidy -diff`。
- `go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

各修复保持按模块隔离；定向测试失败时只回退对应未通过的最小改动，不放宽现有 fail-closed、安全文件、白名单、管理员、binding 或 writer lease 边界。真实 Desktop IPC 和多用户临时目录攻击无法在单元测试中完全覆盖，将作为剩余实机验证边界报告。

### Review 小结（2026-08-05）

- 新增审批默认拒绝审计覆盖取消和超时分支；卡片/文本审批、管理命令、身份授权/撤销和停止动作均有结构化记录，摘要不包含正文、授权码或命令输出。
- `go test ./messaging -count=1 -timeout 180s`、`go test ./... -count=1 -timeout 180s`、核心包 race、`go vet ./...`、`go mod tidy -diff`、文档校验、`gofmt -l` 和 `git diff --check` 均通过。
- `staticcheck ./...` 与 `govulncheck ./...` 已通过；漏洞扫描报告调用路径受影响漏洞为 0。真实 Codex Desktop/daemon 烟测保持显式未验证，不据此宣称实机运行时验收通过。

## 2026-07-26 Codex 官方 daemon 与账号恢复

## 任务清单

- [x] 区分 SQLite 状态库争用、损坏、明确版本不兼容和未知错误，禁止通用错误或 socket 超时触发 CLI 更新。
- [x] 无权威旧账号回滚点时记录可重试的 `external_sync_deferred`，避免错误进入永久 `rollback_failed`。
- [x] 增加 `codex_host_mode`，在官方 standalone 可用时通过 daemon 生命周期连接唯一 Host，且不回退第二 Host。
- [x] 验证 daemon backend、socket、PID、启动时间、受管路径和 generation，并让 Web 配置完整保留新字段。
- [x] 完成定向、全仓、race、vet、staticcheck、依赖差异、文档门禁、四目标交叉构建和独立交付复核。
- [ ] 在不影响活动任务的隔离窗口执行真实 daemon start/version/stop 与账号切换 smoke；GitHub CI 漏洞扫描已通过，本地 `govulncheck` 仍需在明确允许向外部漏洞库发送依赖元数据后执行。

## 当前状态

实现与本地可执行门禁已完成，独立复核为有条件通过。当前开发机已安装官方 standalone，且现有 WeClaw 后台服务运行正常；本轮没有为验证而中断活动服务，因此隔离的 daemon 生命周期与账号切换 smoke 仍待执行。Codex Desktop 与受管 daemon 的连接状态必须继续独立核验，不能只根据 App UI 推断。
