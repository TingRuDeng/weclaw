# Requirements Document

## Introduction

本 spec 针对 `multi-platform-feishu` 实现完成后代码审查发现的遗留问题做收口修复。范围聚焦飞书平台的权限错误处理、CardKit 卡片生命周期、富文本解析噪声，以及几处工程打磨项。**不改动平台抽象层和微信行为，不引入新功能**，只做修复与加固。

每条修复以 `go build ./...`、`go vet ./...`、`go test ./...`（含 `-race`）全绿为验收门槛。

权威参考：工作区外参考项目 `/Volumes/Data/code/tmp/open-im/src/feishu/permission.ts`（已在生产使用，含权威权限错误码集合与降级发送链），Codex 执行时用终端访问。

## Glossary

- **权限错误码**：飞书开放平台在应用缺少 scope/能力/上架授权时返回的 `code`。
- **降级发送链**：发送指引时优先卡片、失败后退化为纯文本的策略。
- **CardKit**：飞书卡片 2.0，weclaw 用于流式打字机与按钮。

## Requirements

## Requirement 1: 飞书权限错误码识别准确且完整

**User Story:** 作为飞书用户，当应用缺少权限时，我希望系统能准确识别并给出开通引导，而不是把普通参数错误误判为权限问题、或漏判真正的权限错误。

#### Acceptance Criteria

1. THE SYSTEM SHALL 将权限错误码集合扩展为与参考实现一致：`99991400`、`99991401`、`99991663`、`99991672`、`99991670`、`99991668`。
2. WHEN 飞书 API 错误无法提取到 `code` THE SYSTEM SHALL 基于错误文本做兜底判断（命中 `permission`/`权限`/`scope`/`forbidden`/`not authorized`/`no access` 等关键词即视为权限错误）。
3. THE 权限错误判定 SHALL 收敛在 `feishu/permission.go` 单一入口（`IsPermissionError(err)` 或等价函数），供发送、CardKit、校验各处复用。
4. THE 现有 `feishu/permission_test.go` SHALL 扩充覆盖：6 个权限码全部命中、非权限码（如普通参数/系统错误）不误判、无 code 时按文本兜底命中与不命中两类用例。

_对应审查项 P1-2_

## Requirement 2: 权限不足时尽力把开通引导发送到聊天

**User Story:** 作为飞书用户，我希望在聊天里直接看到"缺哪些权限、去哪开通"，而不是只能去看服务端日志。

#### Acceptance Criteria

1. WHEN 飞书发送消息因权限不足失败 THE SYSTEM SHALL 在控制台日志之外，尽力通过飞书 API 把权限开通引导发送到当前会话。
2. THE 引导发送 SHALL 采用降级链：先尝试卡片，失败后退化为纯文本；两者都失败时 SHALL 仅记录日志且不向上抛出二次错误。
3. THE 引导内容 SHALL 包含权限设置页直达链接（`https://open.feishu.cn/app/{appId}/permission`）和所需 scope 列表（`im:message`、`im:message:send_as_bot`、`im:resource`、`im:chat`，以及可选 `cardkit:card`）。
4. THE 引导发送 SHALL 复用现有 60s 冷却（`permissionGuideLimiter`），避免刷屏。
5. THE 引导发送 SHALL NOT 在日志或消息中泄露 `app_secret`。

_对应审查项 P2-4_

## Requirement 3: 明确 CardKit 卡片生命周期回收

**User Story:** 作为维护者，我希望卡片资源要么被真正回收，要么明确依赖飞书侧 TTL，不留下"看起来销毁了其实没有"的误导实现。

#### Acceptance Criteria

1. THE SYSTEM SHALL 核实 `larksuite/oapi-sdk-go/v3` 的 cardkit v1 是否提供卡片删除/失效接口。
2. IF SDK 提供删除接口 THEN THE `DestroyCard` SHALL 调用真实接口完成销毁。
3. IF SDK 不提供删除接口 THEN THE `DestroyCard` 的注释 SHALL 明确说明"依赖飞书侧卡片实例 TTL 自动回收，无需主动删除"，并去除"保留钩子供后续替换"这类暗示未完成的措辞。
4. THE 行为变更 SHALL NOT 破坏现有 `feishu/stream_test.go` 与 CardKit 相关测试。

_对应审查项 P2-3_

## Requirement 4: 清理飞书富文本(post)解析注入的无效占位

**User Story:** 作为 agent，我希望收到的富文本消息文本干净可用，而不是夹带对我无意义的 `image_key` 占位。

#### Acceptance Criteria

1. WHEN 解析飞书 `post` 富文本且其中含图片/文件资源 THE SYSTEM SHALL NOT 向交给 agent 的文本注入 Markdown 图片占位或 `<file key="..."/>` 这类无效占位。
2. THE 图片/文件资源 SHALL 仍按现有逻辑下载为本地附件并通过 `Attachments` 传递；agent 文本中如需提及附件，SHALL 使用与普通图片/文件入站一致的占位或本地路径表述。
3. THE `feishu/incoming_test.go` SHALL 更新断言以反映清理后的文本输出。

_对应审查项 新发现-1_

## Requirement 5: 卡片回调访问控制前不暗示成功

**User Story:** 作为对安全敏感的用户，我不希望未授权用户点击卡片按钮也收到"已收到"的成功反馈。

#### Acceptance Criteria

1. WHEN 卡片 `card.action.trigger` 回调触发 THE SYSTEM SHALL 在返回成功 toast 前校验操作者是否在访问控制白名单内。
2. IF 操作者不在白名单 THEN THE SYSTEM SHALL 返回中性或拒绝类 toast（不暗示操作已被接受），并 SHALL NOT 触达 agent/审批逻辑。
3. THE 白名单判定 SHALL 复用现有 `platform` 访问控制，不在 feishu 包内重复实现一套。

_对应审查项 新发现-2_

## Requirement 6: 启动时输出飞书权限要求提示

**User Story:** 作为首次接入飞书的用户，我希望服务启动时就看到需要开通哪些权限，而不是等到发消息失败。

#### Acceptance Criteria

1. WHEN 飞书 adapter 启动并通过凭证校验 THE SYSTEM SHALL 在日志中输出所需 scope 清单与权限设置页链接（参考 open-im `logPermissionGuide`）。
2. THE 输出 SHALL 仅在启动时进行一次，不在每条消息时重复。

_对应审查项 P2-4 关联_

## Requirement 7: 排查并缓解 config 包测试缓慢

**User Story:** 作为维护者，我希望测试套件不被单个包拖到几十秒，保证 CI 反馈及时。

#### Acceptance Criteria

1. THE SYSTEM SHALL 定位 `config` 包测试耗时偏高（约 40s）的根因（如真实探测本机 agent 二进制 / 文件系统遍历 / sleep）。
2. THE 修复 SHALL 在不削弱测试有效性的前提下显著降低耗时（如注入可替换的探测函数、缩短或消除 sleep、限制扫描范围）。
3. THE `config` 包测试 SHALL 在修复后通过且耗时明显下降。

_对应审查项 新发现-3_
