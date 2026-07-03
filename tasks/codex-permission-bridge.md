# Codex 权限模型与审批桥接重做

## 目标

- 重做 WeClaw 的 Codex 权限档位，使配置名和实际 Codex 行为一致。
- 理顺 Codex app-server 的 command / file change 审批事件，飞书只展示真正需要用户决策的审批。
- 避免没有可展示选项的补丁审批被误报成“用户拒绝”。

## 非目标

- 不改变非 Codex ACP Agent 的权限行为。
- 不重做飞书卡片 UI 样式。
- 不修改用户本机 `~/.weclaw/config.json`，只交付代码和迁移说明。

## 当前事实

- `config.AgentConfig.EffectiveApprovalPolicy` 当前把 `auto_approval` 映射为 `untrusted`。
- `config.AgentConfig.EffectiveSandboxMode` 当前把 `auto_approval` 映射为 `danger-full-access`。
- `agent.ACPAgent.getOrCreateThread` / `resumeThread` / `chatCodexAppServerWithRetry` 会把映射后的 `approvalPolicy` 与 `sandbox` 传给 Codex app-server。
- `agent.ACPAgent.handlePermissionRequest` 只从 `options` / `availableDecisions` 生成飞书审批选项。
- `agent.ACPAgent.resolvePermissionOption` 当前在没有选项时直接返回 `decline`，导致 Codex 把补丁结果记为被拒绝。
- 日志中出现过 `FileChangeRequestApprovalResponse` 只接受 `accept / acceptForSession / decline / cancel` 的证据，说明 file change 审批必须按 Codex 协议返回合法 decision。

## 决策日志

- 推荐把权限档位改成三档：
  - `request_approval`: `approvalPolicy=on-request`，`sandbox=workspace-write`。
  - `auto_approval`: `approvalPolicy=never`，`sandbox=workspace-write`。
  - `full_access`: `approvalPolicy=never`，`sandbox=danger-full-access`。
- 保留 `approval_policy` / `sandbox_mode` 作为高级覆盖，显式配置优先。
- Codex 提供 `availableDecisions` 时必须原样回传对应 decision，不再自行发明 `deny`。
- Codex 未提供可选 decision 的 file change 审批不弹飞书卡片；记录明确日志并用协议拒绝值结束。

## 执行计划

- [x] 串行：更新 `config/config.go` 的权限档位映射与注释。
- [x] 串行：更新 `config/config_test.go` 覆盖新三档语义和高级覆盖优先级。
- [x] 串行：更新 `agent/acp_agent.go` 的审批决策解析，确保 command / file change fallback 使用协议合法值。
- [x] 串行：补 `agent/approval_test.go` 覆盖 file change 无 options、有 `availableDecisions`、unroutable fallback 的响应。
- [x] 串行：检查 `messaging/handler.go` 的 yolo 自动审批是否只返回 Codex 提供的 allow 类 decision。
- [x] 串行：补 `messaging/handler_test.go` 或 `messaging/chat_control_test.go` 覆盖 yolo 对 file change 使用 `accept`。
- [x] 串行：更新 `/mode`、`/status`、`/help` 中权限档位文案，避免继续暗示 `auto_approval` 是 `danger-full-access`。
- [x] 串行：运行 `go test ./config ./agent ./messaging ./feishu -count=1`。
- [x] 串行：运行 `go test ./... -count=1`。

## 验证结果

- `go test ./config -count=1`：RED 阶段按预期失败，证明 `auto_approval` 旧映射仍是 `untrusted`。
- `go test ./agent -count=1`：RED 阶段按预期失败，证明无 decision 时旧逻辑会返回非法 `deny`。
- `go test ./config ./agent ./messaging ./feishu -count=1`：通过。
- `go test ./... -count=1`：通过。
- `git diff --check`：通过。

## Review 小结

- 权限档位已改为 `request_approval=on-request/workspace-write`、`auto_approval=never/workspace-write`、`full_access=never/danger-full-access`。
- Codex 审批 fallback 不再发明非法 `deny`，优先使用 Codex 提供的拒绝类 decision，否则回 `decline`。
- 飞书 `/mode` 文案已改为“确认模式”，避免和配置级权限模型混淆。
