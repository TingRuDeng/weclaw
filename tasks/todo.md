# 当前任务记录

## 目标

支持通过本机命令行或交互模式配置 Codex 权限档位，避免用户手动编辑 `~/.weclaw/config.json`。

## 非目标

- 不在飞书或微信远程入口中配置 Codex 权限档位。
- 不改变 `/mode` 的会话审批语义。
- 不自动重启 WeClaw。
- 不触碰当前已有未提交改动的飞书任务卡片相关文件。

## 执行任务

- [x] P1 串行：更新任务记录，明确本轮范围、非目标和验证命令。
- [x] P2 串行：新增 `weclaw config permission` 命令实现。
- [x] P3 串行：注册 `weclaw config` 父命令和 `permission` 子命令。
- [x] P4 串行：补充 CLI 权限配置自动化测试。
- [x] P5 串行：补充帮助文本测试。
- [x] P6 串行：运行最小充分验证并执行 review-gate。

## 验证命令

```bash
go test ./cmd ./config -count=1 -timeout 120s
go test ./cmd -run 'Test.*Config.*|TestRootHelpUsesChineseProductDescription' -count=1 -timeout 120s
git diff --check
```

## 进度记录

- 2026-07-08：用户确认分段 Spec 后批准执行，开始按计划串行实现。
- 2026-07-08：已新增本机 `weclaw config permission` 命令、权限档位写入逻辑和自动化测试。
- 2026-07-08：验证通过：`go test ./cmd ./config -count=1 -timeout 120s`、`go test ./cmd -run 'Test.*Config.*|TestRootHelpUsesChineseProductDescription' -count=1 -timeout 120s`、`git diff --check`。

## Review 小结

终态：finished。

Review 小结：实现符合已批准 Spec；新增本机 CLI 配置入口，不暴露飞书/微信远程配置；非法档位、缺失 Agent、交互输入和高级覆盖字段清理均有测试覆盖。Document-refresh: needed。原因：新增了用户可见 CLI 命令，但本轮未被明确要求修改 README，交付说明中先给出用法。
