package messaging

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
)

const accessCodeTTL = 30 * time.Minute

var accessCodeStateMu sync.Mutex

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

type AccessCodeView struct {
	Code      string
	Platform  string
	AccountID string
	UserID    string
	ExpiresAt string
}

// DefaultAccessCodeFile 返回跨平台授权码状态文件路径。
func DefaultAccessCodeFile() string {
	return filepath.Join(defaultDataDir(), "access-codes.json")
}

// ObserveDeniedIdentity 为被拒绝的入站身份生成用户可交给管理员的授权码。
func (h *Handler) ObserveDeniedIdentity(msg platform.IncomingMessage) string {
	if msg.Platform == platform.PlatformFeishu {
		return h.ObserveDeniedFeishuIdentity(msg)
	}
	if msg.Platform != platform.PlatformWeChat {
		return ""
	}
	record, err := issueAccessCode(DefaultAccessCodeFile(), msg, time.Now().UTC())
	if err != nil {
		log.Printf("[access-code] failed to issue code for %s: %v", msg.UserID, err)
		return "当前账号无权限，且授权码生成失败，请联系管理员检查 WeClaw 状态。"
	}
	return fmt.Sprintf("当前账号无权限，请联系管理员授权。\n授权码: %s", record.Code)
}

func issueAccessCode(filePath string, msg platform.IncomingMessage, now time.Time) (accessCodeRecord, error) {
	userID := strings.TrimSpace(msg.UserID)
	if userID == "" {
		return accessCodeRecord{}, fmt.Errorf("用户身份为空")
	}
	accessCodeStateMu.Lock()
	defer accessCodeStateMu.Unlock()
	state, err := loadAccessCodeState(filePath)
	if err != nil {
		return accessCodeRecord{}, err
	}
	purged := purgeExpiredAccessCodes(&state, now)
	if record, ok := findAccessRecord(state, string(msg.Platform), userID, now); ok {
		if purged {
			if err := saveAccessCodeState(filePath, state); err != nil {
				return accessCodeRecord{}, err
			}
		}
		return record, nil
	}
	code, ok := newUniqueAccessCode(state, now)
	if !ok {
		return accessCodeRecord{}, fmt.Errorf("无法生成唯一授权码")
	}
	record := accessCodeRecord{
		Code:      code,
		Platform:  string(msg.Platform),
		AccountID: strings.TrimSpace(msg.AccountID),
		UserID:    userID,
		ExpiresAt: now.Add(accessCodeTTL).UTC().Format(time.RFC3339),
	}
	state.Records[code] = record
	if err := saveAccessCodeState(filePath, state); err != nil {
		return accessCodeRecord{}, err
	}
	return record, nil
}

// ApproveAccessCode 使用通用授权码写入平台 allowed_users。
func ApproveAccessCode(req AccessCodeApprovalRequest) (AccessCodeApprovalResult, error) {
	accessCodeStateMu.Lock()
	defer accessCodeStateMu.Unlock()
	filePath := firstNonBlank(req.FilePath, DefaultAccessCodeFile())
	state, err := loadAccessCodeState(filePath)
	if err != nil {
		return AccessCodeApprovalResult{}, err
	}
	now := time.Now().UTC()
	purged := purgeExpiredAccessCodes(&state, now)
	code := strings.TrimSpace(req.Code)
	record, ok := state.Records[code]
	if !ok || !accessCodeValid(record, now) {
		if purged {
			if err := saveAccessCodeState(filePath, state); err != nil {
				return AccessCodeApprovalResult{}, err
			}
		}
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
	if err := saveAccessCodeState(filePath, state); err != nil {
		return AccessCodeApprovalResult{}, err
	}
	return AccessCodeApprovalResult{Platform: record.Platform, Identity: record.UserID, Admin: req.Admin}, nil
}

// LoadPendingAccessCodeViews 返回仍有效的通用授权码，用于命令行查看待授权用户。
func LoadPendingAccessCodeViews(filePath string) []AccessCodeView {
	accessCodeStateMu.Lock()
	defer accessCodeStateMu.Unlock()
	state, err := loadAccessCodeState(firstNonBlank(filePath, DefaultAccessCodeFile()))
	if err != nil {
		log.Printf("[access-code] failed to load pending codes: %v", err)
		return nil
	}
	now := time.Now().UTC()
	if purgeExpiredAccessCodes(&state, now) {
		if err := saveAccessCodeState(firstNonBlank(filePath, DefaultAccessCodeFile()), state); err != nil {
			log.Printf("[access-code] failed to purge expired codes: %v", err)
		}
	}
	views := make([]AccessCodeView, 0, len(state.Records))
	for _, record := range state.Records {
		if !accessCodeValid(record, now) {
			continue
		}
		views = append(views, AccessCodeView{
			Code:      record.Code,
			Platform:  record.Platform,
			AccountID: record.AccountID,
			UserID:    record.UserID,
			ExpiresAt: record.ExpiresAt,
		})
	}
	return views
}

func loadAccessCodeState(filePath string) (accessCodeState, error) {
	state := accessCodeState{Version: 1, Records: make(map[string]accessCodeRecord)}
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, fmt.Errorf("读取授权码状态失败: %w", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("解析授权码状态失败: %w", err)
	}
	if state.Records == nil {
		state.Records = make(map[string]accessCodeRecord)
	}
	return state, nil
}

func saveAccessCodeState(filePath string, state accessCodeState) error {
	if strings.TrimSpace(filePath) == "" {
		return fmt.Errorf("授权码状态文件路径为空")
	}
	state.Version = 1
	state.Updated = time.Now().UTC().Format(time.RFC3339)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return fmt.Errorf("创建授权码状态目录失败: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("编码授权码状态失败: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(filePath), ".access-codes-*.tmp")
	if err != nil {
		return fmt.Errorf("创建授权码临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := writeAccessCodeTemp(tmp, data); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filePath); err != nil {
		return fmt.Errorf("替换授权码状态失败: %w", err)
	}
	return nil
}

func writeAccessCodeTemp(tmp *os.File, data []byte) error {
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	return tmp.Close()
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

// purgeExpiredAccessCodes 删除过期或格式损坏的记录，并告知调用方是否需要持久化。
func purgeExpiredAccessCodes(state *accessCodeState, now time.Time) bool {
	if state == nil {
		return false
	}
	purged := false
	for code, record := range state.Records {
		if accessCodeValid(record, now) {
			continue
		}
		delete(state.Records, code)
		purged = true
	}
	return purged
}
