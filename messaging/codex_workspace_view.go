package messaging

// codexWorkspaceView 描述 /codex ls 可展示和 /codex switch 可选择的会话条目。
type codexWorkspaceView struct {
	WorkspaceRoot    string
	ThreadID         string
	PendingNewThread bool
	ThreadName       string
	UpdatedAt        string
	Source           string
}
