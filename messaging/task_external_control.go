package messaging

import "strings"

// externalCodexControlState 返回外部任务是否存在、是否可控制以及当前用户是否无权操作。
func (h *Handler) externalCodexControlState(key string, actor string) (bool, bool, bool) {
	h.activeTasksMu.Lock()
	task := h.activeTasks[key]
	if task == nil {
		h.activeTasksMu.Unlock()
		return false, false, false
	}
	task.mu.Lock()
	defer h.activeTasksMu.Unlock()
	defer task.mu.Unlock()
	if task.owner != strings.TrimSpace(actor) {
		return task.externalCodex, task.externalControl, true
	}
	return task.externalCodex, task.externalControl, false
}
