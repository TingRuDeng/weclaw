package messaging

// codexWorkspaceView 描述 Codex/Claude 会话导航使用的归一化条目。
type codexWorkspaceView struct {
	WorkspaceRoot    string
	ThreadID         string
	PendingNewThread bool
	PendingCatalog   bool
	ThreadName       string
	UpdatedAt        string
	Source           string
}
