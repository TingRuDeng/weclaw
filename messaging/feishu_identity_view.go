package messaging

import (
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
)

type FeishuIdentityView struct {
	Key                  string
	DisplayName          string
	UnionID              string
	UserID               string
	OpenID               string
	OpenIDs              map[string]string
	Accounts             []string
	AuthorizedAccounts   []string
	UnauthorizedAccounts []string
	AuthCode             string
	AuthCodeExpiresAt    string
	Pending              bool
	Approved             bool
	Admin                bool
}

// LoadFeishuIdentityViews 读取飞书自动发现身份，供本地 CLI 做只读展示。
func LoadFeishuIdentityViews(filePath string, pendingOnly bool) ([]FeishuIdentityView, error) {
	store := newFeishuIdentityStore()
	store.SetFilePath(firstNonBlank(filePath, DefaultFeishuIdentityFile()))
	if err := store.LoadError(); err != nil {
		return nil, err
	}
	cfg, cfgOK := loadFeishuIdentityConfig()
	records := store.ListRecords()
	views := make([]FeishuIdentityView, 0, len(records))
	for _, record := range records {
		view := feishuIdentityViewFromRecord(record, cfg, cfgOK)
		if pendingOnly && !feishuIdentityViewNeedsApproval(view, record) {
			continue
		}
		views = append(views, view)
	}
	return views, nil
}

// LoadApprovedFeishuIdentityViews 读取已完成授权的飞书身份台账。
func LoadApprovedFeishuIdentityViews(filePath string) ([]FeishuIdentityView, error) {
	store := newFeishuIdentityStore()
	store.SetFilePath(firstNonBlank(filePath, DefaultFeishuIdentityFile()))
	if err := store.LoadError(); err != nil {
		return nil, err
	}
	cfg, cfgOK := loadFeishuIdentityConfig()
	records := store.ListRecords()
	views := make([]FeishuIdentityView, 0, len(records))
	for _, record := range records {
		view := feishuIdentityViewFromRecord(record, cfg, cfgOK)
		if len(view.AuthorizedAccounts) == 0 {
			continue
		}
		views = append(views, view)
	}
	return views, nil
}

func feishuIdentityViewFromRecord(record feishuIdentityRecord, cfg config.Config, cfgOK bool) FeishuIdentityView {
	auth := feishuIdentityAuthorizationForRecord(record, cfg, cfgOK)
	authCode, expiresAt := visibleFeishuAuthCode(record, auth.UnauthorizedAccounts, time.Now().UTC())
	return FeishuIdentityView{
		Key:                  record.Key,
		DisplayName:          record.DisplayName,
		UnionID:              record.UnionID,
		UserID:               record.UserID,
		OpenID:               record.OpenID,
		OpenIDs:              cloneStringMap(record.OpenIDs),
		Accounts:             append([]string(nil), record.Accounts...),
		AuthorizedAccounts:   auth.AuthorizedAccounts,
		UnauthorizedAccounts: auth.UnauthorizedAccounts,
		AuthCode:             authCode,
		AuthCodeExpiresAt:    expiresAt,
		Pending:              record.Pending,
		Approved:             record.Approved,
		Admin:                feishuIdentityAdminForRecord(record, cfg, cfgOK),
	}
}

func feishuIdentityAdminForRecord(record feishuIdentityRecord, cfg config.Config, cfgOK bool) bool {
	if !cfgOK {
		return false
	}
	unionID := strings.TrimSpace(record.UnionID)
	if unionID == "" {
		return false
	}
	return stringSliceContains(cfg.AdminUsers, unionID)
}

func visibleFeishuAuthCode(record feishuIdentityRecord, unauthorized []string, now time.Time) (string, string) {
	if len(unauthorized) == 0 || !feishuAuthCodeValid(record, now) {
		return "", ""
	}
	return record.AuthCode, record.AuthCodeExpiresAt
}

func feishuIdentityViewNeedsApproval(view FeishuIdentityView, record feishuIdentityRecord) bool {
	if strings.TrimSpace(view.AuthCode) != "" {
		return true
	}
	if len(view.UnauthorizedAccounts) == 0 {
		return false
	}
	return true
}

func loadFeishuIdentityConfig() (config.Config, bool) {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return config.Config{}, false
	}
	return *cfg, true
}

// cloneStringMap 复制身份映射，避免 CLI 展示层误改持久化记录。
func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
