# Claude ACP 安装与启动预检

## 目标

在不引入隐式提权和静默降级的前提下，让首次安装自动补齐 Claude ACP 适配器，并确保更新后重启、手动重启不会先停止仍可工作的旧服务再暴露配置问题。

## 已确认行为

- 安装脚本只在检测到 Claude CLI 且未检测到 `claude-agent-acp` 时自动安装适配器。
- 默认安装 `@agentclientprotocol/claude-agent-acp@0.58.1`，允许通过 `CLAUDE_ACP_VERSION` 覆盖。
- `WECLAW_SKIP_CLAUDE_ACP=1` 可显式跳过；无 Claude CLI 的用户不受影响。
- 安装脚本不执行 `sudo npm install`；失败时保留已安装的 WeClaw，但以非零状态返回并给出明确修复命令。
- 已存在的适配器不强制升级，但必须通过能力与配置校验。
- `weclaw update` 不修改全局 npm 包；普通更新成功后提示依赖问题。
- `weclaw update --restart` 与 `weclaw restart` 必须在停止旧服务前完成统一预检，失败时保持旧服务运行。

## 实现计划

- Shell 文件所有权：`install.sh`、`scripts/install_test.sh`、`scripts/release.sh`。
- Go 文件所有权：`config/claude_acp.go`、`cmd/start.go`、`cmd/restart.go`、`cmd/update.go` 及对应测试。
- 文档与任务状态由主流程串行整合，避免并行写冲突。
- 启动预检生成一次有效配置快照和启动闭包，避免停止前后重复加载产生状态漂移。

## 验收标准

- 临时 PATH 与伪命令测试覆盖无 Claude、跳过、已有适配器、安装成功和安装失败。
- 重启与更新后重启的预检失败测试证明停止操作未发生。
- 定向测试、全仓测试、Race、Vet、Staticcheck、文档校验和差异检查全部通过。
- 发布门禁执行安装脚本测试，且测试不得修改真实 npm 全局环境。
