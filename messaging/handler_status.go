package messaging

import (
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
)

// buildStatus 返回用户默认路由的运行状态，供无平台上下文的本地调用使用。
func (h *Handler) buildStatus(userID string) string {
	return h.buildStatusForRoute(userID, userID, "", "")
}

// buildStatusForRoute 展示当前消息会话实际选择的 Agent，而不是全局默认值。
func (h *Handler) buildStatusForRoute(userID string, routeUserID string, platformName platform.PlatformName, accountID string) string {
	currentName := h.defaultAgentNameForRoute(routeUserID, platformName, accountID)
	h.mu.RLock()
	rateLimit := h.rateLimitPerMinute
	auditOn := h.audit != nil
	workspaceConfined := len(h.allowedWorkspaceRoots) > 0
	var ag agent.Agent
	if currentName != "" {
		ag = h.agents[currentName]
	}
	h.mu.RUnlock()

	lines := []string{"WeClaw 运行态"}

	switch {
	case currentName == "":
		lines = append(lines, "agent: none (echo mode)")
	case ag == nil:
		lines = append(lines, "agent: "+currentName+" (not started)")
	default:
		info := ag.Info()
		lines = append(lines, "agent: "+currentName+" ("+info.Type+")", "model: "+agentStatusModelValue(info.Model))
	}

	totalActive, userActive := h.activeTaskCounts(userID)
	lines = append(lines,
		"uptime: "+formatUptime(time.Since(h.startedAt)),
		fmt.Sprintf("running tasks: %d (you: %d)", totalActive, userActive),
		fmt.Sprintf("agent calls: %d, errors: %d", h.agentInvocations.Load(), h.agentErrors.Load()),
	)

	mode := "default"
	if h.isYoloMode(approvalModeKey(userID, routeUserID)) {
		mode = "yolo"
	}
	rateText := "off"
	if rateLimit > 0 {
		rateText = fmt.Sprintf("%d/min", rateLimit)
	}
	lines = append(lines, fmt.Sprintf("mode: %s · rate limit: %s", mode, rateText))
	lines = append(lines, fmt.Sprintf("workspace confined: %t · audit: %t", workspaceConfined, auditOn))

	return wechatCommandText(lines...)
}

// activeTaskCounts 返回当前运行中的任务总数与指定用户的任务数。
func (h *Handler) activeTaskCounts(userID string) (total int, forUser int) {
	owner := strings.TrimSpace(userID)
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	for _, task := range h.tasks.active {
		task.mu.Lock()
		running := taskIsRunningForStatusLocked(task)
		taskOwner := task.owner
		task.mu.Unlock()
		if !running {
			continue
		}
		total++
		if owner != "" && taskOwner == owner {
			forUser++
		}
	}
	return total, forUser
}

// ActiveTaskCount 返回未脱离的运行中任务总数，供本机 CLI 在重启前做保护。
func (h *Handler) ActiveTaskCount() int {
	total, _ := h.activeTaskCounts("")
	return total
}

// formatUptime 以天/时/分粒度展示运行时长。
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// agentStatusModelValue 用明确文案区分空模型配置和真实模型名。
func agentStatusModelValue(model string) string {
	if strings.TrimSpace(model) == "" {
		return "(Agent 默认)"
	}
	return model
}
