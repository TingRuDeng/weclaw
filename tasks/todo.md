# 当前任务记录

## 目标

按深度审查结论顺序修复任务卡片、授权码、文档和复杂度问题。

## 执行任务

- [x] P1 串行：修复普通最终回复进入飞书任务卡片实时状态的问题。
- [x] P2 串行：隐藏已过期飞书授权码，避免 pending/list 输出无效 approve-code。
- [x] P3 串行：补齐微信未授权用户的短期授权码和本地 CLI 授权闭环。
- [x] P4 串行：刷新中文 README 的新安装、飞书授权码和命令说明。
- [x] P5 串行：降低姓名补全对 `approve-code` 授权路径的阻塞影响。
- [x] P6 串行：拆分超限或接近上限文件，满足复杂度约束。
- [x] P7 串行：跑最小充分验证、全量验证和 review-gate。

## 验证命令

```bash
go test ./cmd ./messaging -count=1 -timeout 120s
go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

终态：finished。

Review 小结：普通最终回复不再通过 legacy ACP `agent_message_chunk` 回填到飞书任务卡片；过期飞书授权码不会继续出现在 pending/list 输出；微信未授权用户会收到短期授权码，管理员可通过 `weclaw users approve-code <授权码> [--admin]` 写入微信白名单；中文 README 已同步新安装、飞书授权码和微信授权码流程；CLI 飞书 `approve-code` 先完成授权，再尝试姓名补全；超限文件已拆分。全量测试、race、vet、文档结构校验和 diff 检查已通过。
