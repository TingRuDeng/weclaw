# 当前任务记录

## 2026-09-01 Codex 可用性优先多前端写入

### 目标

让 Codex App、受控 CLI、飞书和微信在同一已验证 Host 上共享 thread 时，普通输入以权威 Host 状态为准，不再被 frontend binding、follower observer 或进度卡同步状态排他阻断。

### 范围与验收标准

- [x] binding 只表示当前窗口选择；Host/目标不可读时保留旧 binding，observer/卡片/历史失败时提交 binding 并标记同步降级。
- [x] 普通输入统一执行权威 `read -> steer/start -> 竞态重读`，不依赖 follower `ready`，不为 Host 不可用的输入排队。
- [x] 写后结果未知只以 turn/items 基线和消息摘要核对；无证据时不自动重试，不持久化完整输入。
- [x] `/cx status` 分开显示绑定、运行通道和进度同步；旧 v14 `preparing/ready` 无需迁移且立即可写。
- [x] 普通 switch/release 不重启 daemon；无人继续观察时调用当前连接的 `thread/unsubscribe`。
- [x] 扩展 official daemon 双客户端协议门禁，覆盖 read 不订阅、并发 start 转 steer、unsubscribe/observer 中断不影响另一客户端和终态补查。
- [x] 更新中英文 README、维护者上下文和长期经验。
- [x] 完成全仓 test、Race、Vet、module tidy、Staticcheck、govulncheck、安装脚本、双平台构建、文档校验和差异复核。
- [ ] 完成本机 Codex App + 飞书真实双向输入、切换及同步失败注入验收；未完成前不得发布。

### 验证方式

```bash
go test ./... -count=1 -timeout 300s
go test -race ./... -count=1 -timeout 480s
go vet ./...
go mod tidy -diff
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
sh scripts/install_test.sh
PYTHONDONTWRITEBYTECODE=1 python3 scripts/validate_docs.py . --profile generic
git diff --check
```

### 回滚

- 代码回滚只恢复旧的 follower-ready 写入门禁和 Host handoff；不得删除 v14 会话状态、input-attempt 元数据、Codex rollout、SQLite 或 terminal outbox。
- 若真实多前端验收暴露上游协议不兼容，保留唯一 Host 和未知交付防重复边界，停止发布并补充兼容修复；不启动第二个 app-server。

### Review

- 实现与自动化门禁已完成：全仓普通测试、全仓 Race、Vet、module tidy、Staticcheck、govulncheck、安装脚本 24 项、文档/格式/差异检查和 darwin/arm64、linux/amd64 构建均通过。
- 最终复核确认：v14 会话格式保持为 14，配置与发布脚本未改；普通 switch/release 不包含 Host 重启，未知交付没有自动重试或完整输入持久化。
- `TestCodexOfficialDaemonTwoClientProtocol` 已扩展但仍是隔离环境 opt-in 门禁，本轮没有用真实 `CODEX_HOME` 执行；Codex App + 飞书双向输入及同步故障注入仍待真机验收，完成前不得发布。
