# 当前任务记录

## 目标

补齐 Codex App 本地发起任务被微信 / 飞书切换接手后的体验：能识别运行中 thread，给出任务进行中提示，支持 `/guide` 引导、`/stop` 停止，并把任务结果回推到原平台。

## 执行任务

- [x] P1 串行：补 agent 层运行中 thread 状态读取测试。
- [x] P2 串行：补 messaging 层切换 active thread 与后续消息提示测试。
- [x] P3 串行：实现 Codex thread 运行态读取与外部任务镜像登记。
- [x] P4 串行：补最小文案与状态展示。
- [x] P5 串行：运行最小充分验证并完成 review-gate。

## 并行评估

本轮不启用 subagent。改动集中在 `agent` 接口、Codex app-server 事件解析、`messaging` 会话切换和任务状态，同一批文件存在写冲突；串行执行更清晰。

## 验证命令

```bash
GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s
GOCACHE=/private/tmp/weclaw-go-cache go vet ./...
python3 scripts/validate_docs.py . --profile generic
git diff --check
```

## Review 小结

已补齐 Codex App 本地运行中会话的接管链路：agent 层新增 `thread/read` 状态解析、`turn/steer`、`turn/interrupt` 和外部 thread watcher；messaging 层在 `/cx switch`、短编号切换和自动切换唯一会话时识别 active thread，登记外部 active task，提示 `/guide` 可发送到当前 Codex App 任务，支持 `/stop` 中断当前 active turn，并在 watcher 完成后回推最终结果。审查中发现状态读取失败和缺失 active turn 不应静默忽略，已改为在切换响应中明确提示失败原因。

验证命令：`GOCACHE=/private/tmp/weclaw-go-cache go test ./agent -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./messaging -count=1 -timeout 60s`、`GOCACHE=/private/tmp/weclaw-go-cache go test ./... -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check`，结果均通过。`agent`、`messaging` 和全仓 Go 测试因本地 listener / 用户状态目录写入需要非沙箱执行，已按权限规则重跑通过。
