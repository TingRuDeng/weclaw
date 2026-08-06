# 当前任务记录

## 2026-08-06 YOLO 自动审批卡片收敛

### 目标

当同一操作者在当前飞书窗口切换 `/mode yolo` 时，既有待审批请求继续自动放行，同时把已经发出的审批卡片更新为明确的自动批准终态、移除操作按钮，并把审批记录写入对应任务卡；YOLO 模式下后续自动审批不再弹新审批卡，只追加任务卡记录。

### 范围与验收标准

- 只处理当前操作者、当前 route 中仍待确认且包含明确允许选项的 Agent 授权；普通结构化提问及其他用户或窗口不受影响。
- 自动审批的真实决策先按现有幂等门禁提交给 Agent，飞书卡片更新不得反向撤销、重复提交或改变审批结果。
- 已存在审批面板时更新为 `已自动批准（YOLO）` 终态并移除按钮，同时在对应任务卡追加同一条简洁记录。
- YOLO 模式下后续授权请求不创建审批面板，只在已有任务卡追加自动审批记录。
- CardKit 更新失败必须可观察：审批保持成功，`/mode` 回复说明卡片更新失败，日志与审计记录失败原因但不记录敏感命令正文。
- 不支持该可选能力的平台保持现有自动审批行为，不制造错误提示或额外消息。

### 实施步骤

- [x] 先补消息层测试，锁定既有审批自动放行后触发一次展示收敛、失败只告警以及未来 YOLO 不弹卡但写记录。
- [x] 先补飞书层测试，锁定审批面板终态、按钮移除、任务卡记录和部分更新失败语义。
- [x] 增加最小平台可选接口并接入 pending approval；复用原任务回复器和 CardKit registry，不引入第二条审批决策链。
- [x] 同步中英文说明与项目上下文，执行定向、全仓、race、vet、tidy、Staticcheck、govulncheck、文档和 diff 验证。

### 验证方式

- `go test ./messaging ./feishu ./platform -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`、`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

如卡片收敛出现回归，移除自动审批展示的可选接口调用即可恢复当前“只自动放行并写审计”的行为；不得回滚或重放已经提交给 Agent 的审批决策，也不删除任务卡、会话历史或审计记录。

### Review 小结（2026-08-06）

- `/mode yolo` 仍由原 pending approval 的原子门禁提交唯一允许决策；已发飞书审批面板或独立卡片随后更新为 `已自动批准（YOLO）` 且移除按钮，对应任务卡追加真实选项和脱敏摘要。
- 后续 YOLO 审批不再发送审批卡；任务卡展示通过有界异步更新完成，不阻塞 Agent 决策。切换瞬间注册的新审批会二次检查 mode，卡片发送失败也不会覆盖已经提交的允许决策。
- CardKit 部分或全部更新失败只影响展示：`/mode` 回复报告失败数量，日志保留平台错误，审计只记录 `card_update_failed/timeout/cancelled` 分类，不记录命令正文。
- 已验证：定向 RED/GREEN、`go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、govulncheck、文档校验与 `git diff --check`。
- 剩余边界：本机未连接真实飞书 CardKit 执行审批，没有验证客户端缓存中的旧卡片动画和网络时序；服务未提交、推送、发布或部署。

## 2026-08-06 Codex / Claude 工作空间与会话命名管理

### 目标

按已确认设计，让管理员通过 WeClaw 登记或隐藏 Codex、Claude 的已有工作目录，并让有权访问目标工作空间的用户重命名 Codex thread 或 Claude session；所有写操作复用 Agent 权威协议、现有单 Host 与 writer lease，不直接修改 Agent 私有状态或删除源码、历史。

### 范围与验收标准

- 独立 `workspace-registry.json` 以规范绝对路径保存 registered/hidden 覆盖层，写入原子、权限为 `0600`，损坏或未知版本时失败关闭且不覆盖原文件。
- `/cx workspace add/remove` 与 `/cc workspace add/remove` 只允许管理员私聊；登记不扩大普通用户的 `allowed_workspace_roots` 权限，隐藏目录不能从列表、编号、ID 或过期卡片绕过。
- `/cx rename current|<编号> <名称>` 通过共享 Codex app-server 的 `thread/name/set` 写入并由 `thread/read.name` 确认；Desktop follower 缺失能力时明确拒绝。
- `/cc rename current|<编号> <名称>` 仅在当前 Claude adapter 公布 `rename` 后，通过同一 ClaudeHost、目标 session writer lease 和 `/rename` 执行，并由 `session/list.title` 读回确认。
- 工作空间移除与会话重命名不改变其他前端 binding，不调用 `thread/delete`、`session/delete`，不启动第二个 Codex/Claude writer。

### 实施步骤

- [x] 先补 registry 合并、幂等、持久化失败、损坏保护和权限过滤的失败测试，再实现最小状态层。
- [x] 先补 workspace add/remove 解析、管理员私聊、导航合并和隐藏绕过的失败测试，再接入 Codex/Claude 命令。
- [x] 先补 Codex rename 参数、权威 RPC、busy/unknown/Desktop 边界和读回验证测试，再实现消息路由。
- [x] 先补 Claude rename 能力公布、Host 复用、writer lease、读回验证和 binding 不变测试，再实现消息路由。
- [x] 同步帮助、中英文 README 与 `docs/AI_CONTEXT.md`，执行定向、全仓、race、vet、tidy、Staticcheck、文档和 diff 验证。

### 验证方式

- `go test ./agent ./messaging ./cmd -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚策略

先停止暴露 workspace/rename 命令并停用 registry overlay，保留 `workspace-registry.json` 供修复版本恢复；不自动删除登记目录、Agent 会话或历史。重命名失败只重新读取 Agent 权威目录并保留原 binding，不写本地标题补偿。

### Review

- 已实现按 Agent 隔离的工作空间登记/隐藏、管理员私聊门禁、全入口隐藏校验，以及 Codex/Claude 权威协议重命名；未修改或删除工作目录、会话历史和其他窗口 binding。
- 已验证：`go test ./agent ./messaging ./cmd -count=1 -timeout 180s`、`go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`、`go vet ./...`、`go mod tidy -diff`、Staticcheck、文档校验与 `git diff --check`。
- 剩余边界：未连接真实 Codex app-server 或 Claude ACP adapter 做线上重命名；运行时继续以能力公布和权威读回失败关闭，不返回假成功。

## 2026-08-06 首次安装依赖选择向导

### 目标

让普通用户首次安装 WeClaw 后立即看到缺失依赖及其用途，按需要选择 Codex、Claude 和辅助能力；安装器在展示完整命令与权限影响并取得确认后，按依赖顺序安装、重新探测并配置可用 Agent。

### 当前事实与推荐交互

- WeClaw 正式二进制本身为静态程序，没有必须额外安装的 Agent 运行时；Node.js/npm、Codex、Claude、Claude ACP、SQLite 和 bubblewrap 是否必需取决于用户选择的 Agent 与功能。
- 首次安装向导先按“所选能力的必要依赖”和“可选增强”分组展示。选择 Codex 或 Claude 后，Node.js/npm 等前置项自动加入并标记为联动必要项，不能静默遗漏。
- `curl | sh` 的标准输入属于安装脚本，交互必须显式连接可用的 `/dev/tty`；没有 TTY 时只打印只读检查结果和可复制的非交互命令，不自动安装任何包。
- 系统包可以在确认后显式使用 `sudo`；npm 包禁止使用 `sudo npm`。当前 npm prefix 不可写时使用用户级目录并通过绝对路径完成能力验证和 Agent 配置，不覆盖用户已有 nvm、mise 或 npm 配置。
- Node.js 版本不足且系统包管理器无法提供满足要求的版本时保留真实失败，提示用户使用其已有版本管理方式处理；不自动添加第三方软件源。
- Claude/Codex 登录、OAuth、API Token 或中继凭据不由安装器自动创建，只在安装和能力检查通过后提示用户完成认证。

### 验收标准

- 交互式首次安装完成二进制校验后，展示缺失组件、用途、必要/可选关系和选择编号；直接回车取消，不产生额外系统或 npm 写入。
- 用户选择 Codex 时按顺序处理 Node.js/npm、Codex CLI 和 `app-server` 验证；选择 Claude 时按顺序处理 Node.js/npm、Claude CLI、固定版本 ACP adapter 和 initialize 验证。
- SQLite 与 Linux bubblewrap 作为可选增强单独展示，不因用户选择 Agent 而被无条件安装。
- 执行前完整显示包管理器、npm 命令、是否需要 sudo 和目标安装目录；用户拒绝时保留已安装的 WeClaw 二进制并正常结束向导。
- 普通用户面对 root 所有的 npm prefix 时不会尝试 `sudo npm`，而是使用受保护的用户级目录；安装后当前进程能解析并保存 Agent 的绝对命令路径。
- 非交互安装必须显式提供组件列表和确认参数；没有 `/dev/tty`、未知组件、版本不足、安装失败或重检失败时均保持可观察错误且不写入假成功配置。
- 已存在且通过能力检查的依赖不重复安装；首次安装和后续 `weclaw doctor --fix` 复用同一组件选择、依赖展开、安装和验证实现。

### 实施步骤

- [x] 先补首次安装交互测试，覆盖 TTY 选择、直接回车取消、无 TTY 提示、组件联动、已有依赖跳过和旧版 Claude 自动安装行为收敛。
- [x] 扩展依赖结果模型与向导输出，明确区分所选能力的必要依赖、可选增强和当前可用项。
- [x] 让 `install.sh` 在二进制安装后通过 `/dev/tty` 进入统一依赖向导；非交互环境只输出带参数的后续命令，不消费脚本输入或默认安装。
- [x] 为 npm 安装增加普通用户目标目录与可写性处理，保持禁止 sudo npm，并用绝对路径完成安装后探测和 Agent 配置。
- [x] 按前置运行时、Agent CLI、ACP adapter、能力探测、配置保存的顺序执行并显示阶段进度；任何阶段失败立即停止后续配置。
- [x] 同步中英文安装说明和项目上下文，移除“检测到 Claude 就未经选择自动安装 ACP”的旧行为描述。
- [x] 执行安装脚本隔离测试、cmd 定向测试、全仓测试、race、vet、Staticcheck、依赖差异、文档校验和 diff 复核。

### 验证方式

- `sh scripts/install_test.sh`。
- `go test ./cmd ./config -count=1 -timeout 180s`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

首次安装向导只编排现有 `doctor --fix` 能力。若交互入口回归，可停止从 `install.sh` 调用向导并保留独立的只读 `doctor` 与显式修复命令；不自动卸载已经由系统包管理器或 npm 成功安装的用户软件，也不覆盖用户原有 Agent 配置。

### Review 小结（2026-08-06）

- 首次安装在二进制摘要校验后先运行只读 Doctor；有控制终端时通过 `/dev/tty` 进入统一选择向导，无终端时只打印安全引用的显式命令。旧版二进制不支持 `doctor --fix` 时只提示更新，不调用未知参数。
- 向导用角色标签区分可选增强、安装前置、可选 Agent 和 Claude 必需 adapter；选择 Codex/Claude 会自动展开前置项，macOS 不展示 Linux 专属 bubblewrap，用户拒绝或直接回车不安装。
- 普通用户的 npm Agent 包使用 `~/.local` prefix，不使用 `sudo npm`；用户可写的 `~/.local/bin` 只在所有安装命令完成后临时加入 PATH，用于重检和保存绝对 Agent 路径，不参与 sudo 或系统包管理器解析。
- 安装脚本 21 个隔离用例、全仓普通测试与 race、vet、`go mod tidy -diff`、Staticcheck、govulncheck、文档校验和 `git diff --check` 均通过；govulncheck 报告调用路径 0 项漏洞。
- 审查结论为通过。本机测试没有执行真实 apt/dnf/Homebrew/npm 安装，也没有进行 Codex、Claude 登录或远端服务变更；外部包管理器行为仍由目标机器环境决定并保持真实失败语义。

## 2026-08-05 运行依赖诊断与交互修复

### 目标

让 WeClaw 在用户实际触发 Agent 或 Codex 会话目录前发现缺失依赖，并在用户明确选择和确认后安装受支持组件、重新探测能力并补齐 Agent 配置。

### 范围与安全边界

- `weclaw doctor` 默认保持只读，检查 `sqlite3`、Linux Codex 沙箱使用的 `bubblewrap`、Node.js/npm、Codex CLI、Claude Code CLI 和 Claude ACP adapter。
- 区分阻塞依赖、可选功能依赖和未选择的 Agent：已配置 Agent 的运行依赖缺失为失败；`sqlite3`、`bubblewrap` 或未配置 Agent 缺失只提示功能影响和可修复入口。
- `weclaw doctor --fix` 仅安装用户选中的缺失组件；交互终端逐项选择并在执行前显示来源、命令和权限影响，默认拒绝。
- 非交互环境必须同时显式提供组件列表与 `--yes`；禁止因为 stdin 不是终端而默认安装全部组件。
- 系统包仅使用已识别平台的原生包管理器和固定参数；需要提权时显式调用 `sudo`，不收集或保存密码。
- Node.js/npm 作为 npm 安装链的条件依赖：Codex 或 Claude ACP 选择 npm 安装路径且本机缺失时自动加入待选依赖，但仍必须由用户在最终确认清单中授权。
- Node.js/npm 只通过已识别平台的原生包管理器安装，并验证上游要求的最低版本；不自动添加 NodeSource 等第三方软件源，不擅自替换 nvm、mise、Homebrew 或其他现有版本管理器管理的安装。
- Codex、Claude 与 Claude ACP 只使用固定的官方发布入口或项目已固定的 adapter 包版本；不接受用户输入拼接包名、URL 或 shell 命令。
- 安装完成后必须重新执行相同能力探测；Codex 验证 `app-server`，Claude 分别验证 CLI 与 ACP 初始化。登录和账号授权不自动执行，只输出后续登录提示。
- 不自动修改系统软件源，不绕过 TLS、签名、摘要或代理策略；原生包管理器不能提供满足版本要求的 Node.js/npm 时保留真实失败，并提示用户通过已使用的版本管理器先完成安装。
- 不在测试中执行真实 `sudo`、包管理器、npm 或远程安装脚本；所有外部动作经依赖注入验证参数、确认门禁和重检行为。

### 验收标准

- 当前 Jump Server 缺少 `sqlite3` 的状态会在 `weclaw doctor` 中提前显示，说明只影响 Codex 会话目录；安装并验证后变为 `[ok]`。
- Linux 上 Codex 可运行但缺少 `bubblewrap` 时显示安全能力降级警告，并可单独选择安装。
- Node.js 与 npm 分别检测命令和版本；仅使用现有 Agent 时不把未使用的 npm 标记为阻塞，选择 Codex npm 安装或 Claude ACP 时会显式展示并联动选择缺失前置项。
- 已配置但缺少 Codex、Claude ACP 等运行命令时保持 `[fail]`；未配置且未安装的 Codex/Claude 只作为可选组件列出。
- `--fix` 只执行选中项；用户拒绝、未知组件、不支持平台、缺少包管理器、安装命令失败或安装后探测仍失败时返回可观察错误，不写入假成功配置。
- Codex 安装后验证 CLI 和 `app-server` 能力；Claude 安装后验证 CLI，再补齐并验证固定版本 ACP adapter，成功后使用现有配置原子写入路径注册 Agent。
- 安装脚本与 CLI 对依赖状态和修复方式的提示一致；中英文使用说明明确安装行为、权限边界、认证边界和非交互参数。

### 实施步骤

- [x] 增加依赖描述与探测层，复用 `config.LookPath`，为 SQLite 数据库、Node.js/npm 版本、Codex `app-server`、Claude CLI/ACP 和 Linux `bubblewrap` 补充无副作用能力检查。
- [x] 先补 `doctor` 结果分级、组件选择、默认拒绝、非交互门禁、平台映射、安装失败与重检失败测试。
- [x] 为 `doctor` 增加 `--fix`、`--components`、`--yes`，解析组件依赖图并展示联动选择，通过固定参数直接调用受支持的包管理器或安装入口，禁止动态 shell 拼接。
- [x] 安装成功后复用正式 Agent 检测和配置保存路径；只在能力探测通过后写入 Codex/Claude 配置，并提示用户完成官方登录。
- [x] 调整 `install.sh`：安装 WeClaw 后只汇总缺失依赖和修复入口，不在 `curl | sh` 默认流程中新增未经选择的系统软件安装。
- [x] 同步 `README_CN.md`、`README.md` 与项目上下文中的依赖、诊断和安装说明。
- [x] 执行定向测试、安装脚本测试、全仓测试、race、vet、Staticcheck、依赖差异、文档校验和 diff 复核。

### 验证方式

- `go test ./cmd ./config ./messaging -count=1 -timeout 180s`。
- `sh scripts/install_test.sh`。
- `go test ./... -count=1 -timeout 180s`、`go test -race ./... -count=1 -timeout 240s`。
- `go vet ./...`、`go mod tidy -diff`、`go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

依赖探测、安装执行和 Agent 配置写入保持分层。若修复入口出现回归，可移除 `--fix` 路径并保留只读诊断；任何安装或重检失败均不得写入成功状态或覆盖原有 Agent 配置。系统包和第三方 CLI 由各自包管理器安装，WeClaw 不在回滚时擅自卸载用户机器上已有或新安装的软件。

### Review 小结（2026-08-05）

- `weclaw doctor` 默认只读，提前报告 Codex `app-server`、`sqlite3` 只读 `quick_check`、Linux `bubblewrap`、Node.js/npm、Claude CLI 与 ACP adapter；非原生 `codex-acp` 配置不会被误判为缺少 Codex CLI 的阻断错误。
- `doctor --fix` 支持交互编号选择，以及非交互 `--components ... --yes`；依赖联动、固定参数命令、默认拒绝、安装后重检和 Agent 能力通过后配置均有自动化覆盖，外部安装测试不执行真实 sudo/npm。
- Codex 通过官方 npm 包安装并验证 `app-server`；Claude CLI 与固定 `claude-agent-acp@0.58.1` 使用 npm 且禁止 sudo，Claude 路径要求 Node.js 22+。系统包管理器无法满足版本时在 npm 前失败，不添加第三方软件源。
- 安装脚本 24 个隔离用例、全仓测试、全仓 race、vet、`go mod tidy -diff`、Staticcheck、文档校验和 `git diff --check` 均通过；没有执行真实系统包或 Agent 安装，也没有自动登录外部账号。

## 2026-08-05 GitHub/Gitee 双源安装与更新

### 目标

在保持 GitHub 为权威发布源的前提下，为中国大陆或受限网络用户提供经过同一摘要校验的 Gitee 安装与更新镜像，并让来源选择、切换原因和镜像异常保持显式可见。

### 范围与安全边界

- 安装器支持显式 `github`、`gitee` 与 `auto` 来源；Gitee 用户可从 Gitee 源码镜像取得安装脚本，不再依赖 `raw.githubusercontent.com`。
- `weclaw update` 使用与安装器一致的来源语义；默认来源和环境覆盖必须有明确优先级，不能让未知值静默回落 GitHub。
- `auto` 仅在 DNS、连接、TLS、超时或服务端不可用时切换镜像，并输出切换原因；摘要缺失、重复、格式非法或 SHA-256 不匹配时立即失败，禁止换源掩盖供应链异常。
- GitHub 继续负责版本、构建和权威 Release；Gitee 只镜像同一批二进制与 `checksums.txt`，不在 Gitee 重新构建。
- 正式 GitHub Release 成功后再执行 Gitee 镜像；Gitee 失败不得删除已公开的 GitHub Release，但发布结果必须明确标记镜像降级并返回可观察失败。
- 不引入第三方公共加速代理，不把 Gitee Token 写入仓库、日志、命令输出、Release 资产或配置示例。
- 保留现有 128 MiB 下载上限、临时文件、摘要校验、原子替换、备份和回滚语义；Gitee latest 落后时禁止把现有客户端降级。

### 关键未知与执行门禁

- 目标仓库已确认为公开仓库 `git@gitee.com:jimdeng891/weclaw.git`；当前为空仓库，尚无默认分支或 Release。
- Gitee 官方 API v5 已确认提供创建 Release、上传附件、查询 latest、列出/下载附件端点；写操作需要外部注入访问令牌。
- 发布自动化使用用户创建的 Gitee 私人令牌，权限限制为 `user_info` 与 `projects`；令牌已保存为 GitHub Actions Secret `GITEE_TOKEN`，不在对话、本机配置或仓库中传递明文。
- 本机首次 SSH 探测因 Gitee host key 尚未进入 `known_hosts` 而失败；不使用 `StrictHostKeyChecking=no` 绕过。自动化优先走 HTTPS + Secret，避免把本机 host key 接受作为发布依赖。
- 在令牌安全注入、用户批准本计划且最小实测窗口确认前，不修改实现代码、不推送 Gitee、不上传 Release。

### 验收标准

- GitHub 可用时默认安装与更新行为保持兼容；受控模拟 GitHub 网络失败时，`auto` 明确切换到 Gitee 并完成同一版本安装或更新。
- 显式 Gitee 来源完全不访问 GitHub；显式 GitHub 来源完全不访问 Gitee。
- 两个来源都覆盖四个正式目标和同名 `checksums.txt`；缺失资产、错误摘要、截断下载、超限响应、未知来源和镜像版本倒退均失败关闭。
- `weclaw update` 从 Gitee 下载后仍执行现有启动预检、运行态路径核对、原子替换和失败回滚。
- 发布镜像只接受权威流程已生成并验证的资产；上传后重新下载并校验 tag、资产集合、文件大小和 SHA-256。
- 中英文 README、安装帮助、更新配置、项目上下文和发布运维说明与实际行为一致。

### 实施步骤

- [ ] 确认 Gitee 仓库、令牌最小权限、Release/API 能力和一个不含凭据的测试版本上传/下载流程。
- [x] 先扩展 `scripts/install_test.sh` 与 `cmd/update_test.go`，锁定三种来源、允许换源的网络错误、禁止换源的完整性错误和防降级语义。
- [x] 在 `install.sh` 抽取 release provider，加入来源选择、显式日志、Gitee latest/资产 URL 与同源摘要校验。
- [x] 在 `cmd/update_release.go`、`cmd/update.go` 和配置/Web 映射中加入 update source，复用现有下载上限、摘要校验、原子安装和回滚路径。
- [x] 增加独立的 Gitee 镜像发布脚本或 workflow 步骤：只上传 GitHub 权威流程的现有资产，上传后重新下载验证；不在镜像侧构建。
- [x] 同步 `README_CN.md`、`README.md`、`docs/AI_CONTEXT.md` 和发布运维说明，明确 GitHub 权威、Gitee 镜像及失败边界。
- [ ] 执行安装/更新定向测试、全仓测试、race、vet、Staticcheck、govulncheck、文档校验、diff 检查和两个来源的隔离下载烟测。

### 验证方式

- `sh scripts/install_test.sh`，覆盖 GitHub/Gitee/auto、网络失败切换、摘要失败不切换、架构映射和显式版本。
- `go test ./cmd -count=1 -timeout 120s`，覆盖 provider URL、latest 解析、版本防降级、下载上限、摘要和回滚。
- 使用本地受控 HTTP server 分别模拟 GitHub 与 Gitee，不依赖公共网络制造成功测试或故意超时。
- 对真实 Gitee 测试 Release 仅执行一次最小上传、下载和摘要烟测；删除或保留测试 Release 由用户在执行前确认。
- `go test ./... -count=1 -timeout 120s`、`go test -race ./... -count=1 -timeout 180s`、`go vet ./...`、`go mod tidy -diff`。
- `go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...`、`go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。

### 回滚与修正策略

双源逻辑按安装、更新和镜像发布三个边界隔离。实现回退时保留现有 GitHub 路径和校验语义，移除 Gitee provider/config/workflow 即可恢复单源；任何 Gitee 上传或校验失败都不修改已公开 GitHub Release。镜像凭据只通过外部 secret 注入，撤销凭据即可停止后续镜像，不影响现有客户端使用 GitHub。

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

- [x] 先补临时 Codex home/SQLite/rollout fixture，锁定 dry-run、双向 provider 切换、ID 修复和恢复行为。
- [x] 实现输入校验、只读盘点、writer 门禁、空间检查、备份清单和 staged 原子替换。
- [x] 实现 SQLite、全部 `session_meta` 与可选 catalog 同步，以及类型特定的 item ID 修复与碰撞校验。
- [x] 实现备份恢复入口和幂等校验，不自动操作 Codex/WeClaw 进程。
- [x] 执行 shell 语法/静态检查、fixture 测试、文档校验和 diff 复核。

### 验证方式

- `bash -n scripts/codex-provider-switch.sh scripts/codex_provider_switch_test.sh`。
- `bash scripts/codex_provider_switch_test.sh`。
- 若本机存在 ShellCheck，执行 `shellcheck scripts/codex-provider-switch.sh scripts/codex_provider_switch_test.sh`。
- `PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic`、`git diff --check`。
- 只对备份副本执行真实数据 dry-run；未经单独确认不关闭当前 Codex App、不改写真实 `~/.codex`。

### 回滚与修正策略

每次写入前对 SQLite 使用一致性备份，对所有将修改的 rollout 和可选 catalog 建立独立备份与清单。恢复入口只接受脚本生成且校验通过的备份目录；恢复时同样要求无 writer，并通过 staged 替换和 SQLite integrity check 完成。任何中途失败保留备份和错误日志，不自动重试、不自动启动服务。

### Review 小结（2026-08-05）

- `scripts/codex-provider-switch.sh` 默认只读预览；`--apply` 前同时检查 Codex/WeClaw 进程、目标目录打开文件、SQLite writer、文件归属、磁盘空间和备份目录安全性。
- provider 切换同步覆盖 `state_5.sqlite`、可选 Desktop catalog、当前与归档 rollout 的全部 `session_meta`；显式 `--repair-item-ids` 仅修复已知类型的 `item_` 前缀并保留 `call_id`。
- fixture 覆盖 dry-run、双向切换、归档、多条元数据、ID 引用、幂等、备份恢复、writer 门禁、错误 JSON、ID 碰撞、损坏备份和路径约束；全部测试通过。
- 对真实 `~/.codex` 的临时一致性副本执行 dry-run 成功：复制 27 个 rollout，识别 15 个待改文件、215 条 `session_meta` 和 63 个待修复 item ID；真实目录未写入。
- `bash -n`、嵌入 Python 语法编译、文档校验、fixture 测试与 diff 检查通过；本机未安装 ShellCheck，因此按“若存在则执行”的验证约定未运行。加密 reasoning 仍明确告警，跨 provider 完整兼容不作保证。

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
