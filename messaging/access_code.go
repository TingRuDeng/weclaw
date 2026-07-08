package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

const accessCodeTTL = 30 * time.Minute

type accessCodeState struct {
	Version int                         `json:"version"`
	Records map[string]accessCodeRecord `json:"records"`
	Updated string                      `json:"updated"`
}

type accessCodeRecord struct {
	Code      string `json:"code"`
	Platform  string `json:"platform"`
	AccountID string `json:"account_id,omitempty"`
	UserID    string `json:"user_id"`
	ExpiresAt string `json:"expires_at"`
}

type AccessCodeApprovalRequest struct {
	Code     string
	Admin    bool
	FilePath string
}

type AccessCodeApprovalResult struct {
	Platform string
	Identity string
	Admin    bool
}

// DefaultAccessCodeFile 返回跨平台授权码状态文件路径。
func DefaultAccessCodeFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".weclaw", "access-codes.json")
}

// ObserveDeniedIdentity 为被拒绝的入站身份生成用户可交给管理员的授权码。
func (h *Handler) ObserveDeniedIdentity(msg platform.IncomingMessage) string {
	if msg.Platform == platform.PlatformFeishu {
		return h.ObserveDeniedFeishuIdentity(msg)
	}
	if msg.Platform != platform.PlatformWeChat {
		return ""
	}
	record, ok := issueAccessCode(DefaultAccessCodeFile(), msg, time.Now().UTC())
	if !ok {
		return ""
	}
	return fmt.Sprintf("当前账号无权限，请联系管理员授权。\n授权码: %s", record.Code)
}

func issueAccessCode(filePath string, msg platform.IncomingMessage, now time.Time) (accessCodeRecord, bool) {
	userID := strings.TrimSpace(msg.UserID)
	if userID == "" {
		return accessCodeRecord{}, false
	}
	state := loadAccessCodeState(filePath)
	if record, ok := findAccessRecord(state, string(msg.Platform), userID, now); ok {
		return record, true
	}
	code, ok := newUniqueAccessCode(state, now)
	if !ok {
		return accessCodeRecord{}, false
	}
	record := accessCodeRecord{
		Code:      code,
		Platform:  string(msg.Platform),
		AccountID: strings.TrimSpace(msg.AccountID),
		UserID:    userID,
		ExpiresAt: now.Add(accessCodeTTL).UTC().Format(time.RFC3339),
	}
	state.Records[code] = record
	saveAccessCodeState(filePath, state)
	return record, true
}

// ApproveAccessCode 使用通用授权码写入平台 allowed_users。
func ApproveAccessCode(req AccessCodeApprovalRequest) (AccessCodeApprovalResult, error) {
	filePath := firstNonBlank(req.FilePath, DefaultAccessCodeFile())
	state := loadAccessCodeState(filePath)
	code := strings.TrimSpace(req.Code)
	record, ok := state.Records[code]
	if !ok || !accessCodeValid(record, time.Now().UTC()) {
		return AccessCodeApprovalResult{}, fmt.Errorf("授权码无效或已过期")
	}
	if record.Platform != string(platform.PlatformWeChat) {
		return AccessCodeApprovalResult{}, fmt.Errorf("该授权码不支持通用授权命令")
	}
	cfg, err := config.Load()
	if err != nil {
		return AccessCodeApprovalResult{}, err
	}
	platformCfg := cfg.Platforms[record.Platform]
	platformCfg.AllowedUsers = appendUniqueString(platformCfg.AllowedUsers, record.UserID)
	cfg.Platforms[record.Platform] = platformCfg
	if req.Admin {
		cfg.AdminUsers = appendUniqueString(cfg.AdminUsers, record.UserID)
	}
	if err := config.Save(cfg); err != nil {
		return AccessCodeApprovalResult{}, err
	}
	delete(state.Records, code)
	saveAccessCodeState(filePath, state)
	return AccessCodeApprovalResult{Platform: record.Platform, Identity: record.UserID, Admin: req.Admin}, nil
}

func loadAccessCodeState(filePath string) accessCodeState {
	state := accessCodeState{Version: 1, Records: make(map[string]accessCodeRecord)}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(data, &state); err != nil || state.Records == nil {
		state.Records = make(map[string]accessCodeRecord)
	}
	return state
}

func saveAccessCodeState(filePath string, state accessCodeState) {
	if strings.TrimSpace(filePath) == "" {
		return
	}
	state.Version = 1
	state.Updated = time.Now().UTC().Format(time.RFC3339)
	_ = os.MkdirAll(filepath.Dir(filePath), 0o700)
	data, err := json.MarshalIndent(state, "", "  ")
	if err == nil {
		_ = os.WriteFile(filePath, data, 0o600)
	}
}

func findAccessRecord(state accessCodeState, platformName string, userID string, now time.Time) (accessCodeRecord, bool) {
	for _, record := range state.Records {
		if record.Platform == platformName && record.UserID == userID && accessCodeValid(record, now) {
			return record, true
		}
	}
	return accessCodeRecord{}, false
}

func newUniqueAccessCode(state accessCodeState, now time.Time) (string, bool) {
	for i := 0; i < 10; i++ {
		code, ok := randomFeishuAuthCode()
		if ok && !accessCodeExists(state, code, now) {
			return code, true
		}
	}
	return "", false
}

func accessCodeExists(state accessCodeState, code string, now time.Time) bool {
	record, ok := state.Records[code]
	return ok && accessCodeValid(record, now)
}

func accessCodeValid(record accessCodeRecord, now time.Time) bool {
	expiresAt, err := time.Parse(time.RFC3339, record.ExpiresAt)
	return err == nil && strings.TrimSpace(record.Code) != "" && now.Before(expiresAt)
}
