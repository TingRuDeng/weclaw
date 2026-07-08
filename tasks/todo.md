# 当前任务记录

## 目标

实现飞书多机器人用户身份自动发现与管理员确认授权：自动记录待授权身份，管理员确认后写入配置，降低多 bot 下手工维护 `open_id` 的成本。

## 执行任务

- [x] P0 串行：新增身份状态模型与持久化。
- [x] P1 串行：在 Registry 拒绝前记录飞书身份。
- [x] P2 串行：启动时串联身份 store、Handler 和 Registry。
- [x] P3 串行：实现管理员确认命令。
- [x] P4 串行：修复管理命令身份判断。
- [x] P5 串行：确认授权后复用配置热更新。
- [x] P6 串行：CLI 辅助查看身份。
- [x] P7 串行：公开说明与回归验证。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./platform ./messaging ./cmd -run 'Test.*FeishuIdentity|Test.*Admin.*Union|Test.*Registry.*Identity' -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./platform ./messaging ./cmd -count=1 -timeout 60s
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

终态：finished。

Review 小结：自动发现只记录身份，不自动授权；管理员确认后写配置并复用现有热重载。所有计划验证已通过。
