package messaging

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type runningTaskView struct {
	agentName      string
	preview        string
	elapsed        time.Duration
	lastProgress   string
	lastProgressAt time.Time
	stoppable      bool
}

// handleListActiveTasks 列出指定用户当前运行中的后台任务，供 /ps 查看。
func (h *Handler) handleListActiveTasks(userID string) string {
	now := time.Now()
	tasks := h.runningTasksForOwner(strings.TrimSpace(userID), now)
	if len(tasks) == 0 {
		return "当前没有运行中的任务。"
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].elapsed > tasks[j].elapsed })
	lines := []string{fmt.Sprintf("运行中的任务（%d）：", len(tasks))}
	for i, task := range tasks {
		lines = append(lines, renderRunningTask(i, task, now))
	}
	return strings.Join(append(lines, runningTasksFooter(tasks)), "\n")
}

// runningTasksForOwner 复制指定用户可展示的任务状态，避免渲染阶段持锁。
func (h *Handler) runningTasksForOwner(owner string, now time.Time) []runningTaskView {
	var tasks []runningTaskView
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	for _, task := range h.tasks.active {
		task.mu.Lock()
		if task.owner == owner && taskIsRunningForStatusLocked(task) {
			tasks = append(tasks, runningTaskView{
				agentName: task.agentName, preview: task.preview,
				elapsed: now.Sub(task.startedAt), lastProgress: task.view.lastProgress,
				lastProgressAt: task.view.lastProgressAt,
				stoppable:      !task.isExternalCodexLocked() || task.canControlExternalCodexLocked(),
			})
		}
		task.mu.Unlock()
	}
	return tasks
}

func taskIsRunningForStatusLocked(task *activeAgentTask) bool {
	return task != nil && !task.detached && task.phase != codexTaskTerminal
}

// renderRunningTask 渲染单个任务的耗时、摘要和最近进展。
func renderRunningTask(index int, task runningTaskView, now time.Time) string {
	name := firstNonBlank(task.agentName, "agent")
	line := fmt.Sprintf("%d. %s · 已运行 %s", index+1, name, formatTaskElapsed(task.elapsed))
	if preview := strings.TrimSpace(task.preview); preview != "" {
		line += "\n   " + preview
	}
	if progress := strings.TrimSpace(task.lastProgress); progress != "" {
		line += fmt.Sprintf("\n   最近进展（%s前）：%s", formatTaskElapsed(now.Sub(task.lastProgressAt)), progress)
	}
	return line
}

// runningTasksFooter 根据任务控制能力展示准确的停止提示。
func runningTasksFooter(tasks []runningTaskView) string {
	stoppable := false
	readOnly := false
	for _, task := range tasks {
		stoppable = stoppable || task.stoppable
		readOnly = readOnly || !task.stoppable
	}
	switch {
	case stoppable && readOnly:
		return "\n/stop 仅停止可控制任务。"
	case stoppable:
		return "\n回复 /stop 停止当前任务。"
	default:
		return "\n任务完成后结果会自动返回当前会话。"
	}
}

// formatTaskElapsed 以分钟/秒粒度展示任务已运行时长。
func formatTaskElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d秒", int(d.Seconds()))
	}
	return fmt.Sprintf("%d分%d秒", int(d.Minutes()), int(d.Seconds())%60)
}
