package messaging

const feishuIdentityStoreVersion = 1

type feishuIdentityState struct {
	Version int                             `json:"version"`
	Records map[string]feishuIdentityRecord `json:"records"`
	Updated string                          `json:"updated"`
}

type feishuIdentityRecord struct {
	Key               string            `json:"key"`
	DisplayName       string            `json:"display_name,omitempty"`
	UnionID           string            `json:"union_id,omitempty"`
	UserID            string            `json:"user_id,omitempty"`
	OpenID            string            `json:"open_id,omitempty"`
	OpenIDs           map[string]string `json:"open_ids,omitempty"`
	Accounts          []string          `json:"accounts,omitempty"`
	AuthCode          string            `json:"auth_code,omitempty"`
	AuthCodeExpiresAt string            `json:"auth_code_expires_at,omitempty"`
	Pending           bool              `json:"pending"`
	Approved          bool              `json:"approved"`
	FirstSeen         string            `json:"first_seen,omitempty"`
	LastSeen          string            `json:"last_seen,omitempty"`
}

type feishuIdentityCandidate struct {
	Key       string
	AccountID string
	OpenID    string
	UserID    string
	UnionID   string
}
