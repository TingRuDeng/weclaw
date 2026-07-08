# 当前任务记录

## 目标

修复飞书已读事件日志报错、普通文本污染卡片展示、任务卡片实时状态不准确的问题。

## 执行任务

- [x] P1 串行：定位飞书日志、回复卡片化和实时状态根因。
- [x] P2 串行：补飞书已读事件、Codex 计划进度、实时状态筛选回归测试。
- [x] P3 串行：注册飞书 `im.message.message_read_v1` 空处理器。
- [x] P4 串行：接入 Codex App `turn/plan/updated` 并只把结构化进度写入任务卡片。
- [x] P5 串行：进度渲染优先展示 `进展：` 状态，避免最终回复正文覆盖。
- [x] P6 串行：全量验证与 review-gate。

## 验证命令

```bash
go test ./feishu ./platform ./messaging ./cmd ./web -count=1 -timeout 120s
go test ./... -count=1 -timeout 120s
go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

终态：finished。

Review 小结：已读事件只做显式 no-op，不改变业务消息流；Codex App 最终回复不再写入任务卡片实时状态，实时区优先显示结构化 `进展：` 信息。全量测试、vet、文档校验和 diff 检查已通过。
