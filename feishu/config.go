package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	weclawconfig "github.com/fastclaw-ai/weclaw/config"
)

const (
	envAppID     = "WECLAW_FEISHU_APP_ID"
	envAppSecret = "WECLAW_FEISHU_APP_SECRET"
)

var tenantTokenURL = "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal"

// Credentials 保存飞书应用凭证，禁止写入普通 config.json。
type Credentials struct {
	AppID     string `json:"app_id"`
	AppSecret string `json:"app_secret"`
}

// CredentialRecord 带来源信息，供 status 命令展示。
type CredentialRecord struct {
	Credentials Credentials
	Source      string
	Path        string
}

// CredentialsPath 返回飞书凭证文件路径。
func CredentialsPath() (string, error) {
	home, err := weclawconfig.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "platforms", "feishu.json"), nil
}

// CredentialsPathForBot 返回指定飞书机器人的凭证文件路径。
func CredentialsPathForBot(name string) (string, error) {
	cleanName, err := normalizeBotName(name)
	if err != nil {
		return "", err
	}
	home, err := weclawconfig.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "platforms", "feishu", cleanName+".json"), nil
}

// SaveCredentials 以 0600 权限保存飞书凭证。
func SaveCredentials(creds Credentials) error {
	if err := validateLocalCredentials(creds); err != nil {
		return err
	}
	path, err := CredentialsPath()
	if err != nil {
		return err
	}
	return writeCredentialsFile(path, creds)
}

// SaveCredentialsForBot 以 0600 权限保存指定飞书机器人的凭证。
func SaveCredentialsForBot(name string, creds Credentials) error {
	if err := validateLocalCredentials(creds); err != nil {
		return err
	}
	path, err := CredentialsPathForBot(name)
	if err != nil {
		return err
	}
	return writeCredentialsFile(path, creds)
}

// LoadCredentials 读取飞书凭证，环境变量优先于文件。
func LoadCredentials() (Credentials, error) {
	record, err := LoadCredentialsWithSource()
	if err != nil {
		return Credentials{}, err
	}
	return record.Credentials, nil
}

// LoadCredentialsForBot 读取指定飞书机器人的凭证。
func LoadCredentialsForBot(name string) (Credentials, error) {
	record, err := LoadCredentialsWithSourceForBot(name)
	if err != nil {
		return Credentials{}, err
	}
	return record.Credentials, nil
}

// LoadCredentialsWithSource 读取飞书凭证并返回来源。
func LoadCredentialsWithSource() (CredentialRecord, error) {
	if record, ok, err := credentialsFromEnv(); ok || err != nil {
		return record, err
	}
	path, err := CredentialsPath()
	if err != nil {
		return CredentialRecord{}, err
	}
	record, err := readCredentialsFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CredentialRecord{}, fmt.Errorf("feishu credentials not found, run `weclaw feishu login --app-id <id> --app-secret <secret>`")
		}
		return CredentialRecord{}, err
	}
	return record, nil
}

// LoadCredentialsWithSourceForBot 读取指定飞书机器人凭证并返回来源。
func LoadCredentialsWithSourceForBot(name string) (CredentialRecord, error) {
	path, err := CredentialsPathForBot(name)
	if err != nil {
		return CredentialRecord{}, err
	}
	record, err := readCredentialsFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CredentialRecord{}, fmt.Errorf("feishu bot %q credentials not found, run `weclaw feishu login --name %s --app-id <id> --app-secret <secret>`", name, name)
		}
		return CredentialRecord{}, err
	}
	return record, nil
}

// ValidateCredentials 调用飞书 tenant token 接口校验 app_id/app_secret 是否有效。
func ValidateCredentials(ctx context.Context, creds Credentials) error {
	if err := validateLocalCredentials(creds); err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{
		"app_id":     creds.AppID,
		"app_secret": creds.AppSecret,
	})
	if err != nil {
		return fmt.Errorf("marshal feishu validation request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tenantTokenURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create feishu validation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("validate feishu credentials: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("validate feishu credentials: http status %d", resp.StatusCode)
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode feishu validation response: %w", err)
	}
	if result.Code != 0 {
		return formatFeishuAPIError(creds.AppID, result.Code, result.Msg)
	}
	return nil
}

// credentialsFromEnv 从环境变量读取凭证；只配置一半时直接报错，避免误用旧文件凭证。
func credentialsFromEnv() (CredentialRecord, bool, error) {
	appID := strings.TrimSpace(os.Getenv(envAppID))
	appSecret := strings.TrimSpace(os.Getenv(envAppSecret))
	if appID == "" && appSecret == "" {
		return CredentialRecord{}, false, nil
	}
	creds := Credentials{AppID: appID, AppSecret: appSecret}
	if err := validateLocalCredentials(creds); err != nil {
		return CredentialRecord{}, true, err
	}
	return CredentialRecord{Credentials: creds, Source: "env"}, true, nil
}

func writeCredentialsFile(path string, creds Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create feishu credentials dir: %w", err)
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal feishu credentials: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write feishu credentials: %w", err)
	}
	return nil
}

func readCredentialsFile(path string) (CredentialRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CredentialRecord{}, fmt.Errorf("read feishu credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return CredentialRecord{}, fmt.Errorf("parse feishu credentials: %w", err)
	}
	if err := validateLocalCredentials(creds); err != nil {
		return CredentialRecord{}, err
	}
	return CredentialRecord{Credentials: creds, Source: "file", Path: path}, nil
}

func normalizeBotName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("invalid feishu bot name %q", name)
	}
	for _, r := range name {
		if isBotNameRune(r) {
			continue
		}
		return "", fmt.Errorf("invalid feishu bot name %q", name)
	}
	return name, nil
}

func isBotNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '-' || r == '_' || r == '.'
}

// validateLocalCredentials 做本地必填校验，不触发网络请求。
func validateLocalCredentials(creds Credentials) error {
	if strings.TrimSpace(creds.AppID) == "" {
		return fmt.Errorf("feishu app_id is required")
	}
	if strings.TrimSpace(creds.AppSecret) == "" {
		return fmt.Errorf("feishu app_secret is required")
	}
	return nil
}
