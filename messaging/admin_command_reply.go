package messaging

import (
	"fmt"
	"strings"
)

// formatServiceAdminCommandReply 把管理命令输出压缩成适合 IM 的可读结果。
func formatServiceAdminCommandReply(command string, output string, err error) string {
	output = strings.TrimSpace(output)
	if err != nil {
		return strings.TrimSpace(fmt.Sprintf("管理命令执行失败：/%s\n错误：%v\n%s", command, err, summarizeServiceAdminOutput(command, output)))
	}
	summary := summarizeServiceAdminOutput(command, output)
	if command == "restart" {
		summary = appendServiceAdminOutput(summary, "重启完成后会自动发送通知。若 30 秒内没有收到完成通知，请查看服务日志确认重启结果。")
	}
	if summary == "" {
		return fmt.Sprintf("管理命令执行完成：/%s", command)
	}
	return fmt.Sprintf("管理命令执行完成：/%s\n%s", command, summary)
}

// formatServiceAdminRestartNotificationUnavailable 明确告知用户本次重启无法自动确认完成状态。
func formatServiceAdminRestartNotificationUnavailable(output string) string {
	summary := appendServiceAdminOutput(
		summarizeServiceAdminOutput("restart", output),
		"重启已触发，但完成通知记录写入失败，请查看服务日志确认重启结果。",
	)
	return fmt.Sprintf("管理命令执行完成：/restart\n%s", summary)
}

// appendServiceAdminOutput 拼接非空命令摘要，避免输出多余空行。
func appendServiceAdminOutput(output string, extra string) string {
	output = strings.TrimSpace(output)
	extra = strings.TrimSpace(extra)
	if output == "" {
		return extra
	}
	if extra == "" {
		return output
	}
	return output + "\n" + extra
}

// summarizeServiceAdminOutput 将原始命令输出压缩成用户需要的关键信息。
func summarizeServiceAdminOutput(command string, output string) string {
	lines := meaningfulOutputLines(output)
	if len(lines) == 0 {
		return ""
	}
	switch command {
	case "update", "upgrade":
		if version := findOutputVersion(lines, "Already up to date ("); version != "" {
			return "当前已是最新版本：" + version
		}
		if hasOutputLinePrefix(lines, "Already up to date") {
			return "当前已是最新版本"
		}
		if version := findOutputVersion(lines, "Updated to "); version != "" {
			return "已更新到：" + version + "\n请执行 /restart --force 生效"
		}
		return lastMeaningfulLine(lines)
	default:
		return truncateRunes(strings.Join(lines, "\n"), 500)
	}
}

// meaningfulOutputLines 去除空行，保留命令输出里的有效信息。
func meaningfulOutputLines(output string) []string {
	rawLines := strings.Split(output, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// findOutputVersion 提取固定前缀后的版本号，用于压缩 update 输出。
func findOutputVersion(lines []string, prefix string) string {
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		version := strings.TrimPrefix(line, prefix)
		version = strings.TrimSuffix(version, ")")
		return strings.TrimSpace(version)
	}
	return ""
}

// hasOutputLinePrefix 判断输出中是否存在指定状态行。
func hasOutputLinePrefix(lines []string, prefix string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// lastMeaningfulLine 返回最后一条有效输出，作为未知命令输出的摘要。
func lastMeaningfulLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return truncateRunes(lines[len(lines)-1], 500)
}
