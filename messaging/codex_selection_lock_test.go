package messaging

func countActiveTasks(h *Handler) int {
	h.activeTasksMu.Lock()
	defer h.activeTasksMu.Unlock()
	return len(h.activeTasks)
}
