package messaging

import "sync"

// taskService owns the active-task index and its outer lifecycle lock. Task
// internals remain protected by activeAgentTask.mu; callers must preserve the
// established lock order: taskService.mu before activeAgentTask.mu.
type taskService struct {
	mu     sync.Mutex
	active map[string]*activeAgentTask
}

func (s *taskService) ensureLocked() {
	if s.active == nil {
		s.active = make(map[string]*activeAgentTask)
	}
}
