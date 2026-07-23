package messaging

func countActiveTasks(h *Handler) int {
	h.tasks.mu.Lock()
	defer h.tasks.mu.Unlock()
	return len(h.tasks.active)
}
