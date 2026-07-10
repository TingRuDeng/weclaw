# 当前任务记录

## 目标

修复 Codex 重连期间错误复用陈旧 stderr，避免 HTTPS 回退完成前提前终止 turn。

## 执行任务

- [x] P1 串行：补充陈旧 stderr 与新版重连错误结构回归测试。
- [x] P2 串行：收紧 stderr 终态条件并识别重连传输事件。
- [x] P3 串行：运行定向测试、全量测试和 review gate。

## 验证命令

```bash
go test ./agent -count=1 -timeout 60s
go test ./... -count=1 -timeout 120s
go vet ./...
git diff --check
```

## 并行说明

本轮不使用 subagent。事件路由、错误解析和测试共享同一状态机，串行修改可避免写冲突。

## 进度记录

- 2026-07-10：远程日志确认空 error 会复用一分钟前的普通 stderr，导致 HTTPS 回退前提前退出，开始补充修复。
- 2026-07-10：P1、P2 完成；新增测试先复现提前终止，再验证普通 stderr 和重连事件均不结束 turn。
- 2026-07-10：P3 完成；全仓测试、Agent race、vet、文档契约和 diff 检查通过。

## Review 小结

终态：finished。

Spec 符合度：陈旧普通 stderr 不再结束 turn，重连错误按非致命事件处理，明确认证与额度错误保持即时返回。

安全检查：未修改权限、配置或外部输入边界，未引入密钥和静默成功路径。

复杂度检查：函数和文件均符合长度、嵌套与参数约束。

Document-refresh: not-needed

原因：本轮只修复内部 Codex 事件归属和终态判断，不改变公开接口。

剩余风险：仍需远程真实 WebSocket 回退验证 HTTPS 完成事件。

潜在技术债：stderr 当前只有最近一条日志，没有 turn 归属元数据，因此只能用于明确可识别错误。

结论：通过。
