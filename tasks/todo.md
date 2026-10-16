# 当前任务记录

## 2026-10-15 Codex 原生客户端架构调整

用户已授权按原生协作方向实施。默认退出 App 启动接管；不新增前端独占、抢占或释放输入权规则。旧共享适配工作由本轮取代，不再作为默认架构待办；Claude 工作继续暂停。

设计：[原生客户端方案](../docs/superpowers/specs/2026-10-15-codex-native-client-design.md)。步骤与验收：[实施计划](../docs/superpowers/plans/2026-10-15-codex-native-client.md)。

- [x] 标准 auto→native；显式 daemon/shared/managed 兼容，旧配置标志保留但不重新启用默认 App 接管。
- [x] native 不写 App 启动环境、不使用 Desktop IPC、不排斥其他 Host、不自动升级 CLI、不迁移历史 provider。
- [x] 按需加载 Codex 并保留默认 Agent 名称；CLI 独立于服务 API 和 lifetime 重启租约。
- [x] 普通 native 服务启停不轮换外部 daemon；显式 force 可在不启动 daemon 的情况下准备维护控制器，旧 pending 记录继续真实恢复。
- [x] 增加显式 app --restore；仅恢复备份中仍匹配应用值的变量，保留用户后来修改，不退出 App。
- [x] 官方 0.162.1 新 daemon 程序/PID 路径兼容；冲突记录仍失败关闭。
- [x] 全仓单元测试、race、vet；配置和恢复入口的定向回归。
- [x] 隔离官方 0.162.1 daemon 双客户端协议门禁：恢复与审批回放、跨端 steer、唯一决策与终态、unsubscribe、并发输入各提交一次及清理通过；本地假模型使用实际 advertised tool。
- [x] Staticcheck、module tidy 与 Linux amd64 交叉构建。
- [x] 文档校验、git diff --check 和最终相关回归；按 review-gate 复核需求、范围、旧模式兼容及数据保护，代码阶段有条件通过，剩余条件为真实前端验收。

2026-10-16 用户已追加授权提交、推送、打包发布；计划发布 v0.1.325。本机 App/服务不自动安装或重启。发布前漏洞门禁发现 Go 1.26.6 标准库漏洞，已将最低 Go 与 CI/发布工具链更新为 1.26.9，修复版扫描通过。真实 App/飞书/微信完整接手尚未验收；独立 App stdio 执行端的内存任务和专属工具不保证迁移。不得把隔离模型测试等同于真实 App 验收。
