# 当前任务记录

## 目标

落实深度审查后的安全、稳定性和发布闭环改造：修复 Codex 权限默认值、命令探测安全、后台启动竞态、mac arm64 更新策略、远程下载复用，并补齐发布后 update smoke。

## 执行任务

- [x] 串行：确认本地和远端分支状态，读取 lessons 与执行约束。
- [x] 串行：P0 修复 Codex 默认沙箱和登录 shell 命令探测风险，并补测试。
- [x] 串行：P1 修复后台启动锁交接竞态和 update 非 mac arm64 提示，并补测试。
- [x] 串行：P2 抽取远程媒体安全下载公共能力，减少平台重复实现，并补测试。
- [x] 串行：P3 统一 GitHub Actions 发布矩阵为 darwin/arm64，并补发布后 `weclaw update` smoke。
- [x] 串行：运行最小充分验证与交付前 review-gate。

## Review 小结

已将 Codex 未显式配置时的默认沙箱从 `danger-full-access` 收紧为 `workspace-write`；登录 shell 二进制探测改为参数传递，避免拼接用户配置 command；后台启动增加 launch lock，并等待子进程持有 runtime lock 后再返回；`weclaw update` 明确只支持当前发布策略的 `darwin/arm64` 资产；新增 `internal/remotefetch` 统一远程媒体安全下载能力，`messaging` 与 `wechat` 改为薄封装；GitHub Actions CI/Release 发布矩阵同步收敛为 `darwin/arm64`；`scripts/release.sh` 在正式发布后会用临时旧版本二进制执行 `weclaw update` smoke，验证下载、checksum 和自替换链路。验证命令：`go test ./... -count=1 -timeout 120s`、`GOCACHE=/private/tmp/weclaw-go-cache go vet ./...`、`PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile scripts/validate_docs.py`、`python3 scripts/validate_docs.py . --profile generic`、`git diff --check`，结果均通过。
