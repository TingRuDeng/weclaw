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
	case "/update", "/restart":
		return true
	default:
		return false
	}
}

// handleServiceAdminCommand 先校验权限和参数，再后台执行受控的 WeClaw 管理命令。
func (h *Handler) handleServiceAdminCommand(ctx context.Context, msg platform.IncomingMessage, routeUserID string, trimmed string, reply platform.Replier) {
	userID := msg.UserID
	if !h.isAdminMessage(msg) {
		sendPlatformText(ctx, reply, userID, adminCommandDeniedText)
		return
	}
	if !isPrivatePlatformMessage(msg, routeUserID) {
		sendPlatformText(ctx, reply, userID, "为避免在群聊中触发主机级操作，请在机器人私聊窗口执行 /update 或 /restart。")
		return
	}
	command, args, err := parseServiceAdminCommand(trimmed)
	if err != nil {
		sendPlatformText(ctx, reply, userID, err.Error())
		return
	}
	if notice, blocked := h.restartBlockedByActiveTasks(command, args); blocked {
		sendPlatformText(ctx, reply, userID, notice)
		return
	}
	executor := h.currentServiceAdminCommandExecutor()
	if executor == nil {
		sendPlatformText(ctx, reply, userID, "管理命令执行器未配置，暂未执行。")
		return
	}
	statusStream := h.openServiceAdminCommandStatus(ctx, command, reply)
	if statusStream == nil {
		sendPlatformText(ctx, reply, userID, "管理命令已受理：/"+command+"，正在后台执行；完成后会另行通知。")
	}
	go h.runServiceAdminCommand(msg, command, args, reply, statusStream)
}

// openServiceAdminCommandStatus 让 /update 在支持流式卡片的平台原地展示检查结果，
// 避免“已是最新版本”被拆到另一条消息后难以发现。
func (h *Handler) openServiceAdminCommandStatus(ctx context.Context, command string, reply platform.Replier) platform.Stream {
	if command != "update" || !reply.Capabilities().Streaming {
		return nil
	}
	stream, err := reply.OpenStream(ctx, platform.StreamOptions{
		Title:          "WeClaw · 更新",
		InitialContent: "更新命令已受理，正在检查本地版本与最新版本，请稍候。\n\n最终结果会在此卡片中更新。",
	})
	if err != nil {
		log.Printf("[admin-update] failed to open status stream, falling back to text: %v", err)
		return nil
	}
	return stream
}

// isAdminUser 判断当前用户是否在管理命令白名单中。
func (h *Handler) isAdminUser(userID string) bool {
	return h.adminIdentityAllowed([]string{userID})
}

// isAdminMessage 使用平台指定的管理身份判断权限；飞书只接受 union_id。
func (h *Handler) isAdminMessage(msg platform.IncomingMessage) bool {
	return h.adminIdentityAllowed(adminIdentityKeysForMessage(msg))
}

// adminIdentityKeysForMessage 返回管理权限可用身份；飞书多应用下只使用稳定 union_id。
func adminIdentityKeysForMessage(msg platform.IncomingMessage) []string {
	if msg.Platform == platform.PlatformFeishu {
		return feishuAdminIdentityKeys(msg)
	}
	return msg.UserIdentityKeys()
}

// feishuAdminIdentityKeys 只提取飞书 union_id，避免 open_id / user_id 跨应用不可复用。
func feishuAdminIdentityKeys(msg platform.IncomingMessage) []string {
	if msg.Metadata != nil {
		if unionID := strings.TrimSpace(msg.Metadata["feishu_union_id"]); unionID != "" {
			return []string{unionID}
		}
	}
	for _, identity := range msg.UserIdentityKeys() {
		identity = strings.TrimSpace(identity)
		if strings.HasPrefix(identity, "on_") {
			return []string{identity}
		}
	}
	return nil
}

func (h *Handler) adminIdentityAllowed(identities []string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, identity := range identities {
		if _, ok := h.adminUsers[strings.TrimSpace(identity)]; ok {
			return true
		}
	}
	return false
}

func (h *Handler) currentServiceAdminCommandExecutor() ServiceAdminCommandExecutor {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.serviceAdminExecutor
}

func (h *Handler) runServiceAdminCommand(msg platform.IncomingMessage, command string, args []string, reply platform.Replier, statusStream platform.Stream) {
	userID := msg.UserID
	h.serviceAdminMu.Lock()
	defer h.serviceAdminMu.Unlock()
	runCtx, cancel := context.WithTimeout(context.Background(), h.adminTimeout)
	defer cancel()
	executor := h.currentServiceAdminCommandExecutor()
	if executor == nil {
		h.finishServiceAdminCommand(runCtx, reply, userID, statusStream, "管理命令执行器未配置，暂未执行。", true)
		return
	}
	output, err := executor(runCtx, command, args)
	if command == "restart" && err == nil {
		if notifyErr := recordAdminRestartNotification(msg); notifyErr != nil {
			log.Printf("[admin-restart] failed to persist completion notification: %v", notifyErr)
			h.finishServiceAdminCommand(runCtx, reply, userID, statusStream, formatServiceAdminRestartNotificationUnavailable(output), true)
			return
		}
	}
	h.finishServiceAdminCommand(runCtx, reply, userID, statusStream, formatServiceAdminCommandReply(command, output, err), err != nil)
}

func (h *Handler) finishServiceAdminCommand(ctx context.Context, reply platform.Replier, userID string, stream platform.Stream, content string, failed bool) {
	if stream == nil {
		sendPlatformText(ctx, reply, userID, content)
		return
	}
	var err error
	if failed {
		err = stream.Fail(ctx, content)
	} else {
		err = stream.Complete(ctx, content)
	}
	if err == nil {
		return
	}
	log.Printf("[admin-update] failed to update status stream, falling back to text: %v", err)
	sendPlatformText(ctx, reply, userID, content)
}

func parseServiceAdminCommand(trimmed string) (string, []string, error) {
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("管理命令为空。")
	}
	command := strings.TrimPrefix(fields[0], "/")
	args := fields[1:]
	switch command {
	case "update":
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

func (h *Handler) restartBlockedByActiveTasks(command string, args []string) (string, bool) {
	if command != "restart" || hasRestartForceArg(args) {
		return "", false
	}
	count := h.activeTaskCount()
	if count > 0 {
		return fmt.Sprintf("当前还有 %d 个运行中的任务，已取消重启。\n\n请等待任务完成或发送 /stop 后重试；如果确认要中断任务并重启，请发送 /restart --force。", count), true
	}
	return "", false
}

func hasRestartForceArg(args []string) bool {
	return len(args) == 1 && args[0] == "--force"
}

func (h *Handler) activeTaskCount() int {
	return h.ActiveTaskCount()
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
		cmd := buildRestartCommand(exe, args)
		if err := cmd.Start(); err != nil {
			log.Printf("[admin] failed to start delayed restart: %v", err)
		}
	}()
	return "已触发 weclaw restart；服务会在消息发出后尝试重启。", nil
}

// buildRestartCommand 构造延迟重启进程；远程重启必须脱离旧服务进程组，避免 stop 超时强杀时把自己杀掉。
func buildRestartCommand(exe string, args []string) *exec.Cmd {
	cmd := exec.Command(exe, append([]string{"restart"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	configureDetachedRestartCommand(cmd)
	return cmd
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
