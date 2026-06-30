# Requirements Document

## Introduction

`weclaw web` 在本机回环地址启动一个轻量配置面板，可视化查看/修改 `~/.weclaw/config.json`、写入飞书凭证、面板内完成微信扫码登录、查看运行状态，并借助现有软配置热重载让大部分修改即时生效。第一性原则是安全：面板能读写含 shell-capable agent 配置与密钥的文件，必须默认仅本机、强制鉴权、密钥只写不回显、写盘原子且 `0600`。

设计见 `design.md`，本需求据其反推。已确认决策：端口 `39282`、飞书校验仅轻量 token 校验、微信扫码在面板内完成。

## Glossary

- **软配置**：可被运行中守护进程 mtime 热重载的配置（progress / default_agent / allowed_users / allowed_workspace_roots / rate_limit）。
- **平台拓扑变更**：平台启用状态或凭证类改动（含新增微信账号），需 `weclaw restart` 接管。
- **掩码常量**：表示"该密钥保持不变"的占位字符串，用于写回。
- **同源防护**：校验请求 Origin/Referer 为本服务地址，防 DNS rebinding/CSRF。

## Requirements

## Requirement 1: 命令与服务启动

**User Story:** 作为用户，我希望运行 `weclaw web` 就能在浏览器里配置 weclaw。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `weclaw web` 命令，默认在 `127.0.0.1:39282` 启动配置面板。
2. THE SYSTEM SHALL 支持 `--addr`（监听地址）、`--token`（鉴权 token）、`--no-open`（不自动打开浏览器）三个 flag。
3. WHEN 未显式提供 token 且监听地址为回环 THE SYSTEM SHALL 自动生成随机会话 token 并在标准输出打印带 token 的本地 URL。
4. WHEN 未提供 `--no-open` THE SYSTEM SHALL 尽力打开默认浏览器到该 URL；打开失败 SHALL NOT 影响服务运行。
5. WHEN 收到 SIGINT/SIGTERM THE SYSTEM SHALL 优雅关闭 HTTP 服务。

_Validates: Property 3_

## Requirement 2: 鉴权与回环安全

**User Story:** 作为对安全敏感的用户，我希望面板默认只能本机访问且需要 token。

#### Acceptance Criteria

1. THE SYSTEM SHALL 默认仅绑定回环地址。
2. WHEN 监听地址为非回环且未提供 token THE SYSTEM SHALL 拒绝启动并提示需要 `--token`。
3. WHEN 请求未携带正确 token THE SYSTEM SHALL 返回 401（token 比较使用常量时间）。
4. WHEN 请求的 Origin/Referer 非本服务地址 THE SYSTEM SHALL 返回 403（同源防护）。
5. THE token 校验与回环判定 SHALL 复用 `api` 包既有逻辑（提取为可共享导出函数），不重复实现。

_Validates: Property 3, Property 4_

## Requirement 3: 读取配置（脱敏）

**User Story:** 作为用户，我希望在面板看到当前配置，但不暴露任何密钥。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `GET /api/config` 返回当前配置的脱敏视图。
2. THE 响应 SHALL NOT 包含明文 `api_token`、agent `api_key`、agent `env` 的值、或飞书 `app_secret`。
3. WHEN 某密钥字段非空 THE 响应 SHALL 以掩码常量或存在性标记表示其"已设置"。
4. THE 响应 SHALL 完整返回非密钥字段（default_agent、platforms、allowed_users、allowed_workspace_roots、rate_limit、progress、agent 的 type/command/model 等）。

_Validates: Property 1_

## Requirement 4: 写回配置（保密 + 原子 + 校验）

**User Story:** 作为用户，我希望在面板改完配置保存后安全落盘且不误清密钥。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `PUT /api/config` 接收脱敏视图并保存。
2. WHEN 某密钥字段的值等于掩码常量 THE SYSTEM SHALL 保留磁盘上该密钥的原值不变。
3. WHEN 某密钥字段为新明文值 THE SYSTEM SHALL 用新值覆盖。
4. WHEN 配置校验失败 THE SYSTEM SHALL 返回 400 + 字段级原因，且 SHALL NOT 写盘。
5. THE 写盘 SHALL 原子完成（临时文件 + rename），失败时磁盘上的 `config.json` 保持修改前内容，权限 `0600`。
6. THE 响应 SHALL 返回 `restart_required`：仅平台拓扑变更为 true，纯软配置变更为 false。

_Validates: Property 2, Property 5, Property 6_

## Requirement 5: 飞书凭证写入与校验

**User Story:** 作为飞书用户，我希望在面板填 app_id/secret 并校验有效性。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `POST /api/feishu/credentials` 把 `app_id`/`app_secret` 写入 `~/.weclaw/platforms/feishu.json`（`0600`），复用 `feishu.SaveCredentials`。
2. THE SYSTEM SHALL NOT 在任何响应中回显 `app_secret`。
3. THE SYSTEM SHALL 提供 `POST /api/validate/feishu` 仅做轻量 token 有效性校验（复用 `feishu.ValidateCredentials`），SHALL NOT 建立 `larkws` 长连接。
4. WHEN 校验失败 THE 响应 SHALL 返回 `{ok:false, message}`（含权限引导信息）。

_Validates: Property 1_

## Requirement 6: 微信面板内扫码登录

**User Story:** 作为用户，我希望在面板里扫码添加微信账号，而不用回终端。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `POST /api/wechat/login/start`，复用 `ilink.FetchQRCode` 返回二维码内容与随机 `login_id`，并在后台用 `ilink.PollQRStatus` 轮询。
2. THE SYSTEM SHALL 提供 `GET /api/wechat/login/status?login_id=` 返回 `waiting`/`scanned`/`confirmed`/`expired`。
3. WHEN 扫码确认 THE SYSTEM SHALL 通过 `ilink.SaveCredentials` 落盘新账号。
4. THE 登录会话 SHALL 存于内存、带 TTL，`login_id` 随机不可猜；过期会话 SHALL 被清理。
5. WHEN `login_id` 非法或过期 THE SYSTEM SHALL 返回过期/未找到状态，且 SHALL NOT 泄露其它会话信息或二维码内容。
6. THE 二维码内容 SHALL NOT 被持久化或写入日志。
7. THE 扫码端点 SHALL 经过与其它 API 相同的 token + 同源中间件。

_Validates: Property 7_

## Requirement 7: 运行状态查看

**User Story:** 作为用户，我希望看到平台启用、凭证存在性、agent 列表与守护进程是否在跑。

#### Acceptance Criteria

1. THE SYSTEM SHALL 提供 `GET /api/status` 返回：各平台 enabled、凭证是否存在、allowed_users 数量；agent 列表（name/type/command 或 endpoint，无密钥）；守护进程是否运行。
2. THE 响应 SHALL NOT 包含任何密钥。

_Validates: Property 1_

## Requirement 8: 前端页面

**User Story:** 作为用户，我希望有一个可用的网页表单来完成上述操作。

#### Acceptance Criteria

1. THE SYSTEM SHALL 通过 `go:embed` 内嵌静态单页与脚本（原生 JS，无构建步骤），由 `GET /` 与 `/static/*` 提供。
2. THE 前端 SHALL 首屏从 URL 的 `?token=` 读取 token 并在后续请求以 `X-WeClaw-Token` 头携带。
3. THE 前端 SHALL 提供：安全卡（allowed_users / allowed_workspace_roots / rate_limit / audit）、agent 卡、平台卡（飞书凭证+开关、微信扫码+状态）。
4. WHEN 保存返回 `restart_required=true` THE 前端 SHALL 提示运行 `weclaw restart`；否则提示已即时生效。

_Validates: Property 1_

## Requirement 9: 质量门槛

**User Story:** 作为维护者，我希望该功能不破坏现有行为且有测试。

#### Acceptance Criteria

1. THE SYSTEM SHALL 使 `go build ./...`、`go vet ./...`、`go test ./...`（含 `-race`）全部通过。
2. THE SYSTEM SHALL 为脱敏、保密写回、原子保存、鉴权（401/403/非回环必鉴权）、restart_required 判定、扫码会话隔离提供单元测试。
3. THE 改动 SHALL NOT 修改平台抽象语义或微信/飞书既有消息处理行为。

_Validates: Property 1, Property 2, Property 3, Property 4, Property 5, Property 7_

## Correctness Properties 交叉引用

| Property | 描述 | 关联需求 |
|----------|------|---------|
| Property 1 | 密钥不外泄 | R3, R5, R7, R8, R9 |
| Property 2 | 掩码即不变 | R4, R9 |
| Property 3 | 回环默认 | R1, R2 |
| Property 4 | 非回环必鉴权 | R2, R9 |
| Property 5 | 原子保存 | R4, R9 |
| Property 6 | 软配置即时性 | R4 |
| Property 7 | 扫码会话隔离 | R6, R9 |
