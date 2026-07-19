package messaging

import "strings"

func buildHelpText() string {
	return buildHelpTextForAdmin(false)
}

func buildHelpTextForAdmin(isAdmin bool) string {
	text := `WeClaw 帮助

常用：

/status 查看 WeClaw 运行态

/new 新建会话

/cwd [路径] 查看或切换当前窗口工作目录

/mode 查看会话审批模式，/mode yolo 当前窗口自动同意 Agent 授权，/mode default 按钮确认

/model、/reasoning 已绑定时修改当前会话，未绑定时修改新会话默认值

/ps 查看运行中的任务

/stop 停止当前运行的任务

Codex：

/cx status 查看 Codex 会话状态

/cx quota 查看 Codex 账号额度

/cx account 查看当前 Codex 账号，管理员私聊可切换

/cx ls 查看列表

/cx <编号|..> 选择或返回

/cx new 新建共享会话

Claude：

/cc quota 查看 Claude 账号额度

发送消息：

/codex <内容> 发给 Codex

@cx <内容> 发给 Codex

/cc <内容> 发给 Claude

@cc @cx <内容> 同时发送

更多：

/cx help Codex 高级命令

/cc help Claude 高级命令

/progress 查看进度模式`
	if !isAdmin {
		return text
	}
	return text + "\n\n" + adminHelpText()
}

func adminHelpText() string {
	return `管理员：

/update 远程更新 WeClaw

/restart 重启 WeClaw

/restart --force 强制重启 WeClaw

/feishu users pending 查看待授权飞书用户

/feishu users list 查看已授权飞书用户

/feishu users approve <用户ID> [--admin] 直接授权飞书用户

/feishu users approve-code <授权码> 授权飞书用户

/feishu users revoke <用户ID> 取消飞书用户授权`
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
