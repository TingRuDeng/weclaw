package messaging

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

const adminCommandDeniedText = "当前账号未授权执行 WeClaw 管理命令，请联系管理员配置 admin_users。"
const adminCommandTimeout = 5 * time.Minute
const adminRestartDelay = 1200 * time.Millisecond

var currentExecutablePathFunc = os.Executable

// ServiceAdminCommandExecutor 执行经过白名单校验的 WeClaw 管理命令。
type ServiceAdminCommandExecutor func(ctx context.Context, command string, args []string) (string, error)

// isServiceAdminCommand 识别会影响 WeClaw 进程或二进制的管理命令。
func isServiceAdminCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "/update", "/upgrade", "/restart":
		return true
	default:
		return false
	}
}

// handleServiceAdminCommand 先校验权限和参数，再后台执行受控的 WeClaw 管理命令。
func (h *Handler) handleServiceAdminCommand(ctx context.Context, userID string, trimmed string, reply platform.Replier) {
	if !h.isAdminUser(userID) {
		sendPlatformText(ctx, reply, userID, adminCommandDeniedText)
		return
	}
	command, args, err := parseServiceAdminCommand(trimmed)
	if err != nil {
		sendPlatformText(ctx, reply, userID, err.Error())
		return
	}
	executor := h.currentServiceAdminCommandExecutor()
	if executor == nil {
		sendPlatformText(ctx, reply, userID, "管理命令执行器未配置，暂未执行。")
		return
	}
	sendPlatformText(ctx, reply, userID, "开始执行管理命令：/"+command)
	go h.runServiceAdminCommand(userID, command, args, reply)
}

// isAdminUser 判断当前用户是否在管理命令白名单中。
func (h *Handler) isAdminUser(userID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.adminUsers[strings.TrimSpace(userID)]
	return ok
}

func (h *Handler) currentServiceAdminCommandExecutor() ServiceAdminCommandExecutor {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.serviceAdminExecutor
}

func (h *Handler) runServiceAdminCommand(userID string, command string, args []string, reply platform.Replier) {
	runCtx, cancel := context.WithTimeout(context.Background(), adminCommandTimeout)
	defer cancel()
	h.serviceAdminMu.Lock()
	defer h.serviceAdminMu.Unlock()
	executor := h.currentServiceAdminCommandExecutor()
	if executor == nil {
		sendPlatformText(runCtx, reply, userID, "管理命令执行器未配置，暂未执行。")
		return
	}
	output, err := executor(runCtx, command, args)
	sendPlatformText(runCtx, reply, userID, formatServiceAdminCommandReply(command, output, err))
}

func parseServiceAdminCommand(trimmed string) (string, []string, error) {
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("管理命令为空。")
	}
	command := strings.TrimPrefix(fields[0], "/")
	args := fields[1:]
	switch command {
	case "update", "upgrade":
		if len(args) > 0 {
			return "", nil, fmt.Errorf("/%s 不支持参数；请先执行 /%s，更新完成后再执行 /restart 或 /restart --force。", command, command)
		}
	case "restart":
		if len(args) > 1 || len(args) == 1 && args[0] != "--force" {
			return "", nil, fmt.Errorf("/restart 只支持可选参数 --force。")
		}
	default:
		return "", nil, fmt.Errorf("未知管理命令：/%s", command)
	}
	return command, args, nil
}

func formatServiceAdminCommandReply(command string, output string, err error) string {
	output = strings.TrimSpace(output)
	if err != nil {
		return strings.TrimSpace(fmt.Sprintf("管理命令执行失败：/%s\n错误：%v\n%s", command, err, summarizeServiceAdminOutput(command, output)))
	}
	if output == "" {
		return fmt.Sprintf("管理命令执行完成：/%s", command)
	}
	return fmt.Sprintf("管理命令执行完成：/%s\n%s", command, summarizeServiceAdminOutput(command, output))
}

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

func hasOutputLinePrefix(lines []string, prefix string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func lastMeaningfulLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return truncateRunes(lines[len(lines)-1], 500)
}

func defaultServiceAdminCommandExecutor(ctx context.Context, command string, args []string) (string, error) {
	if command == "restart" {
		return scheduleRestartCommand(args)
	}
	cliArgs := append([]string{command}, args...)
	cmd := exec.CommandContext(ctx, currentExecutablePath(), cliArgs...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// scheduleRestartCommand 先校验可执行文件，再延迟触发 restart，确保回复能暴露启动前错误。
func scheduleRestartCommand(args []string) (string, error) {
	exe, err := resolveRestartExecutable(currentExecutablePath())
	if err != nil {
		return "", err
	}
	go func() {
		time.Sleep(adminRestartDelay)
		cmd := exec.Command(exe, append([]string{"restart"}, args...)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Printf("[admin] failed to start delayed restart: %v", err)
		}
	}()
	return "已触发 weclaw restart；服务会在消息发出后尝试重启。", nil
}

// resolveRestartExecutable 确认 restart 使用的二进制可访问，避免后台 goroutine 静默失败。
func resolveRestartExecutable(exe string) (string, error) {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return "", fmt.Errorf("restart executable is empty")
	}
	if !strings.ContainsAny(exe, `/\`) {
		resolved, err := exec.LookPath(exe)
		if err != nil {
			return "", fmt.Errorf("restart executable %q not found: %w", exe, err)
		}
		exe = resolved
	}
	info, err := os.Stat(exe)
	if err != nil {
		return "", fmt.Errorf("restart executable %q is not accessible: %w", exe, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("restart executable %q is a directory", exe)
	}
	return exe, nil
}

// currentExecutablePath 返回当前进程路径；探测失败时退回 PATH 里的 weclaw。
func currentExecutablePath() string {
	exe, err := currentExecutablePathFunc()
	if err != nil {
		return "weclaw"
	}
	return exe
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}
