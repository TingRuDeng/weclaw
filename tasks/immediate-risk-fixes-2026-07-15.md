# 第一批高价值风险修复 Spec

## 状态

- 日期：2026-07-15
- 分支：`codex/fix-immediate-risk-batch`
- 阶段：用户已确认，进入按计划执行阶段
- 远端基线：`git fetch origin` 因 SSH 连接被关闭而失败；当前仅确认工作区 `main` 与本地缓存 `origin/main` 一致。

## 目标

在不扩大业务语义的前提下，修复四个已经由代码证据确认、影响明确且可用确定性测试保护的问题：

1. 安装脚本下载二进制后未校验发布资产 SHA-256。
2. 后台启动路径吞掉微信凭据目录读取错误。
3. iLink 会话过期且存在同步游标时错误进入 60 秒退避。
4. 微信 CDN AES-ECB 解密只校验填充长度，不校验完整 PKCS#7 填充字节。

## 非目标

- 不处理微信多账号的主动发送路由、会话键迁移或账号选择协议。
- 不改变 `ilink.LoadAllCredentials` 对单个损坏凭据文件的容错语义。
- 不调整 Web 状态页、Codex Desktop 私有 IPC、Agent 架构或远程抓取策略。
- 不引入发布签名、证书链或新的第三方依赖。
- 不执行发布、推送或合并。

## 当前事实与证据

### 安装链路

- `install.sh:github_download` 负责带可选 GitHub Token 下载资产；主流程下载 `${BINARY}_${OS}_${ARCH}` 后直接 `chmod` 并移动到安装目录，没有读取或验证摘要。
- `scripts/release.sh:build_release_assets` 已生成与二进制同目录的 `checksums.txt`。
- `.github/workflows/ci.yml` 会生成并上传预发布 `checksums.txt`；稳定版 `.github/workflows/release.yml` 委托 `scripts/release.sh` 生成、上传并验证正式 `checksums.txt`。
- `scripts/install_test.sh` 已覆盖平台识别、Token、安装目录和错误分支，但未覆盖资产完整性校验。

### 后台启动

- `cmd/start_runtime.go:runBackgroundStart` 使用 `accounts, _ := ilink.LoadAllCredentials()`，目录读取失败会被当成“没有账号”。
- `cmd/start_runtime.go:loadStartAccounts` 在前台路径会返回同一加载错误，前后台行为不一致。
- `cmd/start_runtime_test.go` 没有验证后台凭据加载失败时必须停止启动。

### iLink 会话退避

- `ilink/monitor.go:(*Monitor).Run` 收到 `errCodeSessionExpired` 后先清空 `m.getUpdatesBuf`，随后再用 `m.getUpdatesBuf == ""` 选择退避，因此原先存在游标时也总会选择 `fatalSessionBackoff`。
- `ilink/monitor_test.go` 只覆盖一般错误的指数退避，没有覆盖会话过期恢复分支。

### CDN 解密

- `wechat/cdn.go:decryptAESECB` 仅检查末字节表示的填充长度是否处于 1 到 AES block size，随后直接裁剪明文。
- 该函数没有确认被裁剪区域的每个字节都等于填充长度，因此损坏或篡改的密文可能被当作有效明文。
- 当前 `wechat/` 测试没有直接覆盖 AES-ECB/PKCS#7 边界。

## 设计原则

- 保持协议与用户可见行为不变，只纠正确认的错误分支。
- 使用现有发布资产与标准库，不引入依赖。
- 测试先行：每个问题先写能稳定失败的回归测试，再写最小实现。
- 错误必须显式暴露，不把真实失败伪装为首次登录或正常启动。
- 新增 helper 只服务于可测试边界，不进行横向架构重构。

## 决策驱动因素

1. 安全影响和运行时影响是否明确。
2. 是否能由当前仓库文件直接证明问题，而非依赖推断。
3. 是否能用无网络、无真实账号、无真实时间等待的测试稳定复现。
4. 是否能将影响控制在单一函数或单一安装链路。
5. 是否会引入配置迁移、协议变更或兼容性设计。

## 方案比较

### 方案 A：聚焦四个确定性问题并逐项 TDD（推荐）

- 优点：风险边界清晰；每项都能单独验证和回滚；不需要配置迁移或外部系统。
- 代价：不会一次清空项目 Top 10 风险，设计型问题继续保留在风险清单。

### 方案 B：一次处理项目 Top 10 风险

- 优点：表面上治理范围更完整。
- 拒绝原因：会混入多账号路由、Agent 状态所有权、Web 写链路等需要先统一语义的问题，无法保持最小影响，也难以在一个批次内完成充分回归。

### 方案 C：只补测试，暂不修实现

- 优点：代码行为不变。
- 拒绝原因：四项问题已有直接代码证据，其中安装完整性和错误退避属于实际缺陷；只记录失败测试不能降低现有风险。

## 关键实现决策

### 1. 安装资产校验

- 修改 `install.sh:github_download` 附近的下载流程：二进制与同版本 `checksums.txt` 都先下载到临时文件。
- 新增小型 shell helper，兼容 `shasum -a 256` 与 `sha256sum`；找不到校验工具、找不到唯一资产条目、摘要格式无效或摘要不一致时均立即失败。
- 校验通过后才允许 `chmod` 和移动目标文件；使用 `trap` 清理临时文件。
- 不做 GPG/Sigstore 签名验证，因为当前发布流程没有对应可信根和签名资产。

### 2. 后台凭据加载错误

- 保留 `cmd/start_runtime.go:runBackgroundStart` 作为真实依赖入口。
- 提取仅供运行编排和测试使用的 options/operations 对象，将凭据加载、登录和 daemon 启动作为函数依赖传入内部 helper。
- 加载错误立即带上下文返回；不得调用登录或 daemon。
- 不使用目录权限技巧制造测试失败，避免测试依赖操作系统用户权限。

### 3. iLink 会话过期恢复

- 在清空游标前记录 `hadSyncBuf`，并由一个小型恢复 helper 统一完成清空、持久化和退避选择。
- 原先有游标时返回 `sessionExpiredBackoff`；原先无游标时返回 `fatalSessionBackoff`。
- 不注入完整时钟或重构 Monitor client，本批次只保护已确认的状态判断错误。

### 4. PKCS#7 校验

- 在 `wechat/cdn.go:decryptAESECB` 已有长度检查后，逐字节验证末尾填充区域。
- 任一字节不等于 `padLen` 时返回错误，不返回部分明文。
- 保持 AES-ECB、密钥长度和上层 CDN 协议不变。

## 执行计划

### P0：规划与隔离（串行）

- [x] 创建 `codex/fix-immediate-risk-batch` 分支。
- [x] 写入本 Spec 与 `tasks/todo.md` 当前任务。
- [x] 用户已显式确认。

### P1：安装校验（串行，测试先行）

- [x] 修改 `scripts/install_test.sh`：让 fake 下载默认生成匹配的 `checksums.txt`。
- [x] 新增 checksum 匹配成功、摘要不匹配且不得替换目标、缺少资产条目的测试。
- [x] 运行新增测试确认 RED。
- [x] 修改 `install.sh` 完成摘要下载、解析、校验和临时文件清理。
- [x] 运行安装脚本测试确认 GREEN。

### P2：后台启动错误（串行，测试先行）

- [x] 在 `cmd/start_background_test.go` 新增加载失败后停止、已有账号直接启动两个行为测试。
- [x] 运行新增测试确认 RED。
- [x] 修改 `cmd/start_runtime.go:runBackgroundStart` 及内部 helper，显式传播加载错误。
- [x] 运行 `cmd` 定向测试确认 GREEN。

### P3：iLink 退避（串行，测试先行）

- [x] 在 `ilink/monitor_test.go` 新增“有游标为 5 秒、无游标为 60 秒”的恢复测试。
- [x] 运行新增测试确认 RED。
- [x] 修改 `ilink/monitor.go:(*Monitor).Run` 及恢复 helper。
- [x] 运行 `ilink` 定向测试确认 GREEN。

### P4：CDN 填充（串行，测试先行）

- [x] 新建 `wechat/cdn_test.go`，覆盖空明文、块边界、跨块往返、损坏填充、非法 key 和非整块密文。
- [x] 运行损坏填充测试确认 RED。
- [x] 修改 `wechat/cdn.go:decryptAESECB` 完成严格 PKCS#7 校验。
- [x] 运行 `wechat` 定向测试确认 GREEN。

### P5：统一验证与审查（串行）

- [x] 运行 shell 语法与安装测试。
- [x] 运行受影响 Go 包测试和 race。
- [x] 运行全量测试、全量 race、vet 与 build。
- [x] 运行文档门禁与 `git diff --check`。
- [x] 使用 `review-gate` 复核 Spec、安全、边界、复杂度和剩余风险。
- [x] 更新本文件与 `tasks/todo.md` 的验证结果和 Review 小结。

## 并行与写冲突评估

- 本批次不启用 subagent。
- 安装、cmd、ilink、wechat 文件理论上可并行，但每项都需要先建立 RED 测试、再最小修复、最后统一验证；任务规模有限，串行可保留清晰的失败证据和提交边界。
- `tasks/todo.md`、统一验证和最终审查均为共享状态，必须由主流程串行整合。

## 验证矩阵

| 范围 | 命令 | 通过标准 |
|---|---|---|
| Shell 语法 | `sh -n install.sh scripts/install_test.sh` | 退出码 0 |
| 安装测试 | `sh scripts/install_test.sh` | 所有案例通过 |
| 后台启动 | `go test ./cmd -run 'TestRunBackgroundStart' -count=1 -timeout 60s` | 新旧相关测试通过 |
| iLink 恢复 | `go test ./ilink -run 'Test.*SessionExpired' -count=1 -timeout 60s` | 两种游标状态退避正确 |
| CDN 加解密 | `go test ./wechat -run 'Test.*AESECB|TestAES.*' -count=1 -timeout 60s` | 往返与非法填充测试通过 |
| 受影响包 | `go test ./cmd ./ilink ./wechat -count=1 -timeout 60s` | 退出码 0 |
| 受影响包 race | `go test -race ./cmd ./ilink ./wechat -count=1 -timeout 120s` | 无失败、无 race |
| 全仓测试 | `go test ./... -count=1 -timeout 120s` | 退出码 0 |
| 全仓 race | `go test -race ./... -count=1 -timeout 180s` | 无失败、无 race |
| 静态检查 | `go vet ./...` | 退出码 0 |
| 构建 | `go build ./...` | 退出码 0 |
| 文档门禁 | `python3 scripts/validate_docs.py . --profile generic` | 退出码 0 |
| Diff | `git diff --check` | 无空白错误 |

## 风险与失败场景

- 旧 GitHub Release 如果缺少 `checksums.txt`，新安装脚本会明确失败；这是完整性校验的预期边界，不做静默降级。
- 某些系统同时缺少 `shasum` 和 `sha256sum` 时安装会明确提示缺少校验工具。
- 后台启动从“误进入登录”改为“返回凭据目录错误”，可能暴露此前被吞掉的权限或文件系统问题；这是有意的错误显化。
- iLink 恢复 helper 若持久化游标失败，当前 `saveBuf` 的日志语义保持不变；本批次不扩大为凭据存储重构。
- 更严格的 PKCS#7 校验可能拒绝过去偶然被接受的损坏密文；有效协议数据不受影响。

## 修正与回滚策略

- 每个问题按独立 RED/GREEN 单元推进；若某项出现需求偏差，停止该项并回到本 Spec 修正，不影响其他项。
- 不使用静默 fallback 绕过 checksum、凭据加载或填充校验失败。
- 需要回滚时按文件和对应测试成对回滚，避免保留失真的测试或无保护实现。

## HARD-GATE

在用户明确回复确认本 Spec 前，不修改 `install.sh`、`scripts/install_test.sh`、`cmd/`、`ilink/`、`wechat/` 中的业务代码或测试代码。

## 验证记录

- P1 RED：`sh scripts/install_test.sh` 退出码 1，新增成功用例明确报告未请求 `/v1.2.3/checksums.txt`。
- P1 GREEN：`sh -n install.sh scripts/install_test.sh` 退出码 0；`sh scripts/install_test.sh` 13 个用例全部通过。
- P2 RED：`go test ./cmd -run TestRunBackgroundStart -count=1 -timeout 60s` 因缺少受控启动编排入口而失败。
- P2 GREEN：同一定向命令通过，加载失败和已有账号两个行为均受保护。
- P3 RED：`go test ./ilink -run 'Test.*SessionExpired' -count=1 -timeout 60s` 因缺少会话过期恢复 helper 而失败。
- P3 GREEN：同一定向命令通过，有游标 5 秒、无游标 60 秒及空游标持久化均受保护。
- P4 RED：`go test ./wechat -run 'Test.*AESECB|TestAES.*' -count=1 -timeout 60s` 显示损坏填充被错误解密为明文。
- P4 GREEN：同一定向命令通过，合法往返、损坏填充、非法密钥和非整块密文均受保护。
- P4 边界 RED：`go test ./wechat -run TestAESECBRejectsInvalidInputs -count=1 -timeout 60s` 显示空密文被错误接受。
- P4 边界 GREEN：AES-ECB 定向命令通过，合法空明文仍可往返，非法空密文被拒绝。
- Shell：`sh -n install.sh scripts/install_test.sh` 通过；`sh scripts/install_test.sh` 13 个用例全部通过。
- 受影响包：`go test ./cmd ./ilink ./wechat -count=1 -timeout 60s` 通过。
- 受影响包 race：`go test -race ./cmd ./ilink ./wechat -count=1 -timeout 120s` 通过。
- 全仓：`go test ./... -count=1 -timeout 120s` 通过。
- 全仓 race：`go test -race ./... -count=1 -timeout 180s` 通过。
- 静态与构建：`go vet ./...`、`go build ./...` 均通过。
- 文档与 diff：`python3 scripts/validate_docs.py . --profile generic`、`git diff --check` 均通过；三个未跟踪新文件单独执行 `git diff --no-index --check` 无空白错误输出。
- 复杂度：本轮涉及文件均不超过 300 行；新增或修改函数均不超过 50 行，参数、嵌套和圈复杂度未越界。
- 安全：变更文件未匹配常见私钥、GitHub Token、AWS Key 或 OpenAI Key 特征。

## 执行中调整

- 复杂度检查发现 `cmd/start_runtime_test.go` 加入本轮测试后超过 300 行，因此将新增测试拆到 `cmd/start_background_test.go`；测试行为、模块范围和验证命令不变。
- Review Gate 发现零长度密文不可能包含合法 PKCS#7 填充，因此在同一 CDN 校验范围内补充空密文拒绝测试与最小实现。

## Review 小结

- 终态：finished。
- Spec 符合度：通过；四项修复和回归测试均位于已批准范围，文件拆分仅用于满足复杂度约束。
- 安全检查：通过；安装资产在替换目标前验证 SHA-256，外部摘要要求唯一且为 64 位十六进制；未引入 secret。
- 测试与验证：通过；四项均有 RED→GREEN 证据，定向、受影响包、全仓、race、vet、build、文档和 diff 门禁全部通过。
- 复杂度检查：通过；文件、函数、嵌套和参数均符合仓库硬约束。
- Document-refresh: not-needed
- 原因：未改变 CLI、配置结构或用户操作流程，checksum 与错误边界属于内部可靠性增强。
- 剩余风险：旧 Release 缺少 `checksums.txt` 时安装会明确失败；checksum 与二进制来自同一 GitHub Release，尚未提供独立签名信任根。
- 潜在技术债：多账号路由、单个损坏凭据文件的容错语义、Codex Desktop 状态所有权等设计型风险仍按非目标保留。
- 结论：通过。
