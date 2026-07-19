package codexauth

import "time"

const indexVersion = 1

type ProfileID string

type SecretBackend string

const (
	SecretBackendKeyring SecretBackend = "keyring"
	SecretBackendFile    SecretBackend = "file"
)

type Profile struct {
	ID                 ProfileID     `json:"id"`
	Label              string        `json:"label"`
	AuthMode           string        `json:"auth_mode"`
	AccountFingerprint string        `json:"account_fingerprint"`
	EmailMasked        string        `json:"email_masked,omitempty"`
	EmailFingerprint   string        `json:"email_fingerprint,omitempty"`
	SecretBackend      SecretBackend `json:"secret_backend"`
	SecretRef          string        `json:"secret_ref"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
	LastUsedAt         *time.Time    `json:"last_used_at,omitempty"`
}

type SwitchRecord struct {
	ProfileID ProfileID `json:"profile_id,omitempty"`
	Status    string    `json:"status"`
	Message   string    `json:"message,omitempty"`
	At        time.Time `json:"at"`
}

type Index struct {
	Version         int           `json:"version"`
	Revision        uint64        `json:"revision"`
	ActiveProfileID ProfileID     `json:"active_profile_id,omitempty"`
	Profiles        []Profile     `json:"profiles"`
	LastSwitch      *SwitchRecord `json:"last_switch,omitempty"`
}

type Status struct {
	HostID      string        `json:"host_id"`
	Revision    uint64        `json:"revision"`
	Current     *Profile      `json:"current,omitempty"`
	Profiles    []Profile     `json:"profiles,omitempty"`
	LastSwitch  *SwitchRecord `json:"last_switch,omitempty"`
	CodexHome   string        `json:"-"`
	SocketPath  string        `json:"-"`
	StorePath   string        `json:"-"`
	AuthPath    string        `json:"-"`
	ManagedHost bool          `json:"managed_host"`
}

type SaveOptions struct {
	Label          string
	Replace        bool
	AllowFileStore bool
}

type DoctorResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	HostID  string `json:"host_id"`
	Store   string `json:"store"`
	Auth    string `json:"auth"`
}
