package messaging

// codexWorkspaceView 描述 /cx ls 可展示和 /cx switch 可选择的会话条目。
type codexWorkspaceView struct {
	WorkspaceRoot    string
	ThreadID         string
	PendingNewThread bool
	ThreadName       string
	UpdatedAt        string
	Source           string
}
