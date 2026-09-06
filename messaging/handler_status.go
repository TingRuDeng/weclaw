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
	version := h.version
	rateLimit := h.rateLimitPerMinute
	auditOn := h.audit != nil
	var ag agent.Agent
	if currentName != "" {
		ag = h.agents[currentName]
	}
	h.mu.RUnlock()

	lines := []string{"WeClaw 运行态", "版本: " + version}

	switch {
	case currentName == "":
		lines = append(lines, "agent: 未配置默认 Agent")
	case ag == nil:
		lines = append(lines, "agent: "+currentName+"（未启动）")
	default:
		info := ag.Info()
		lines = append(lines, "agent: "+currentName+" ("+info.Type+")", "默认模型: "+agentStatusModelValue(info.Model))
	}

	totalActive, userActive := h.activeTaskCounts(userID)
	lines = append(lines,
		"运行时间: "+formatUptime(time.Since(h.startedAt)),
		fmt.Sprintf("运行任务: %d（你的任务: %d）", totalActive, userActive),
		fmt.Sprintf("调用次数: %d，错误次数: %d", h.agentInvocations.Load(), h.agentErrors.Load()),
	)

	mode := "默认"
	if h.isYoloMode(approvalModeKey(userID, routeUserID)) {
		mode = "yolo"
	}
	rateText := "关闭"
	if rateLimit > 0 {
		rateText = fmt.Sprintf("%d/分钟", rateLimit)
	}
	lines = append(lines, fmt.Sprintf("模式: %s · 限流: %s", mode, rateText))
	auditText := "关闭"
	if auditOn {
		auditText = "开启"
	}
	lines = append(lines, "审计: "+auditText)

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
		return "跟随 Agent 默认"
	}
	return model
}
