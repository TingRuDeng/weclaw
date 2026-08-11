package messaging

import "strings"

func buildHelpText() string {
	return buildHelpTextForAdmin(false)
}

func buildHelpTextForAdmin(authorized bool) string {
	text := `WeClaw 帮助

常用：

/status 查看 WeClaw 运行态

/new 新建会话

/cwd [路径] 查看或切换当前窗口工作目录

/mode 查看审批模式，/mode yolo 自动同意当前操作者的 Agent 授权，/mode default 按钮确认

/approve <短码>、/deny <短码> 在审批按钮不可用时提交或拒绝当前窗口审批

/model、/reasoning 已绑定时修改当前会话，未绑定时修改新会话默认值

/fast 切换 Codex 当前会话或新会话默认速度

/ps 查看运行中的任务

/stop 停止当前运行的任务；Codex 共享任务也会在本地端停止

Codex：

/cx status 查看 Codex 会话状态

/cx quota 查看 Codex 账号额度

/cx account 查看当前 Codex 账号，已授权账号私聊可切换

/cx ls 查看列表（编号从 1 开始）

/cx <编号|..> 选择或返回

/cx new 新建共享会话

/cx archive current 归档当前空闲会话

/cx rename current|<编号> <名称> 重命名会话

Claude：

/cc ls 查看列表（编号从 1 开始）

/cc quota 查看 Claude 账号额度

/cc rename current|<编号> <名称> 重命名会话

发送消息：

/codex <内容> 发给 Codex

@cx <内容> 发给 Codex

/cc <内容> 发给 Claude

@cc @cx <内容> 同时发送

更多：

/cx help Codex 高级命令

/cc help Claude 高级命令

/progress 查看进度模式`
	if !authorized {
		return text
	}
	return text + "\n\n" + managementHelpText()
}

func managementHelpText() string {
	return `管理操作：

/update 远程更新 WeClaw（飞书仅已授权账号私聊）

/restart 重启 WeClaw（飞书仅已授权账号私聊）

/restart --force 强制重启 WeClaw（飞书仅已授权账号私聊）

/feishu users pending 查看待授权飞书用户

/feishu users list 查看已授权飞书用户

/feishu users approve <用户ID> 直接授权飞书用户

/feishu users approve-code <授权码> 授权飞书用户

/feishu users revoke <用户ID> 取消飞书用户授权

/cx workspace add <路径> 登记 Codex 工作目录（仅私聊）

/cx workspace remove <编号|路径> 从 WeClaw 移除 Codex 工作目录（仅私聊）

/cx session remove <编号|threadId> 隐藏 Codex 会话，restore 恢复（仅私聊）

/cc workspace add <路径> 登记 Claude 工作目录（仅私聊）

/cc workspace remove <编号|路径> 从 WeClaw 移除 Claude 工作目录（仅私聊）

/cc session remove <编号|sessionId> 隐藏 Claude 会话，restore 恢复（仅私聊）`
}

// wechatCommandText 将内置命令回复转换为空行分隔，避免微信气泡折叠单换行。
func wechatCommandText(parts ...string) string {
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = normalizeCommandNewlines(part)
		for _, line := range strings.Split(part, "\n") {
			line = strings.TrimRight(line, " \t")
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n\n")
}

func normalizeCommandNewlines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}
