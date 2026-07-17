package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	claudeOAuthUsageEndpoint          = "https://api.anthropic.com/api/oauth/usage"
	claudeOAuthUsageBeta              = "oauth-2025-04-20"
	claudeLegacyKeychainService       = "Claude Code-credentials"
	claudeCredentialMaxBytes    int64 = 1 << 20
	claudeQuotaResponseMaxBytes       = 2 << 20
)

var claudeOAuthKnownWindowIDs = []string{
	"five_hour",
	"seven_day",
	"seven_day_oauth_apps",
	"seven_day_opus",
	"seven_day_sonnet",
}

// queryClaudeOAuthQuotaDefault 固定请求 Claude Code 使用的 Anthropic usage 地址，并禁止携带凭据跨域重定向。
func queryClaudeOAuthQuotaDefault(ctx context.Context, accessToken string) (ClaudeQuota, error) {
	return queryClaudeOAuthQuota(ctx, newClaudeQuotaHTTPClient(), claudeOAuthUsageEndpoint, accessToken)
}

func newClaudeQuotaHTTPClient() *http.Client {
	return &http.Client{
		Timeout: claudeQuotaTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func queryClaudeOAuthQuota(ctx context.Context, client *http.Client, endpoint string, accessToken string) (ClaudeQuota, error) {
	if client == nil {
		return ClaudeQuota{}, fmt.Errorf("Claude usage HTTP client is nil")
	}
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return ClaudeQuota{}, fmt.Errorf("Claude OAuth access token is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ClaudeQuota{}, fmt.Errorf("build Claude usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("anthropic-beta", claudeOAuthUsageBeta)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return ClaudeQuota{}, fmt.Errorf("request Claude usage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ClaudeQuota{}, fmt.Errorf("Claude OAuth 登录已失效或无权读取额度 (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return ClaudeQuota{}, fmt.Errorf("Claude usage API returned HTTP %d", resp.StatusCode)
	}
	body, err := readClaudeLimited(resp.Body, claudeQuotaResponseMaxBytes)
	if err != nil {
		return ClaudeQuota{}, fmt.Errorf("read Claude usage response: %w", err)
	}
	return parseClaudeOAuthQuota(body)
}

// readClaudeOAuthAccessToken 只读取 Claude Code 已有登录，不执行登录、刷新或持久化。
func (a *ACPAgent) readClaudeOAuthAccessToken(ctx context.Context) (string, error) {
	if value, ok := a.claudeQuotaEnvValue("CLAUDE_CODE_OAUTH_TOKEN"); ok {
		if token := strings.TrimSpace(value); token != "" {
			return token, nil
		}
	}

	var sourceErrs []error
	if runtime.GOOS == "darwin" {
		data, found, err := a.readClaudeLegacyKeychainCredential(ctx)
		if err != nil {
			sourceErrs = append(sourceErrs, err)
		} else if found {
			token, parseErr := parseClaudeOAuthCredential(data)
			if parseErr == nil {
				return token, nil
			}
			sourceErrs = append(sourceErrs, fmt.Errorf("parse Claude Keychain credentials: %w", parseErr))
		}
	}

	credentialPath, err := a.claudeLegacyCredentialsPath()
	if err != nil {
		sourceErrs = append(sourceErrs, err)
		return "", errors.Join(sourceErrs...)
	}
	data, found, err := a.readClaudeLegacyCredentialFile(ctx, credentialPath)
	if err != nil {
		sourceErrs = append(sourceErrs, err)
	} else if found {
		token, parseErr := parseClaudeOAuthCredential(data)
		if parseErr == nil {
			return token, nil
		}
		sourceErrs = append(sourceErrs, fmt.Errorf("parse Claude credentials file: %w", parseErr))
	}
	return "", errors.Join(sourceErrs...)
}

func (a *ACPAgent) claudeQuotaEnvValue(key string) (string, bool) {
	a.mu.Lock()
	value, ok := a.env[key]
	a.mu.Unlock()
	if a.runAs.shouldIsolate() && !a.runAs.preservesEnv(key) {
		return "", false
	}
	if ok {
		return value, true
	}
	return os.LookupEnv(key)
}

func (a *ACPAgent) readClaudeLegacyKeychainCredential(ctx context.Context) ([]byte, bool, error) {
	data, err := a.runClaudeCredentialCommand(ctx, "/usr/bin/security", []string{
		"find-generic-password", "-s", claudeLegacyKeychainService, "-w",
	})
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil, false, nil
	}
	return data, true, nil
}

func (a *ACPAgent) claudeLegacyCredentialsPath() (string, error) {
	if configDir, ok := a.claudeQuotaEnvValue("CLAUDE_CONFIG_DIR"); ok {
		if configDir = strings.TrimSpace(configDir); configDir != "" {
			return filepath.Join(configDir, ".credentials.json"), nil
		}
	}
	home := ""
	if a.runAs.shouldIsolate() {
		account, err := user.Lookup(strings.TrimSpace(a.runAs.User))
		if err != nil {
			return "", fmt.Errorf("resolve Claude run_as_user home: %w", err)
		}
		home = account.HomeDir
	} else {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve Claude credentials home: %w", err)
		}
	}
	return filepath.Join(home, ".claude", ".credentials.json"), nil
}

func (a *ACPAgent) readClaudeLegacyCredentialFile(ctx context.Context, path string) ([]byte, bool, error) {
	if a.runAs.shouldIsolate() {
		data, err := a.runClaudeCredentialCommand(ctx, "/bin/cat", []string{path})
		if err != nil {
			return nil, false, nil
		}
		return data, true, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read Claude credentials file: %w", err)
	}
	defer file.Close()
	data, err := readClaudeLimited(file, claudeCredentialMaxBytes)
	if err != nil {
		return nil, false, fmt.Errorf("read Claude credentials file: %w", err)
	}
	return data, true, nil
}

func (a *ACPAgent) runClaudeCredentialCommand(ctx context.Context, command string, args []string) ([]byte, error) {
	command, args = a.runAs.wrapCommand(command, args)
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Stderr = io.Discard
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, readErr := readClaudeLimited(stdout, claudeCredentialMaxBytes)
	if readErr != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, readErr
	}
	if err := cmd.Wait(); err != nil {
		return nil, err
	}
	return data, nil
}

func readClaudeLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("payload exceeds %d bytes", maxBytes)
	}
	return data, nil
}

func parseClaudeOAuthCredential(data []byte) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("decode credentials JSON: %w", err)
	}
	for _, key := range []string{"claudeAiOauth", "claude.ai_oauth"} {
		raw, ok := root[key]
		if !ok {
			continue
		}
		var entry struct {
			AccessToken string `json:"accessToken"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return "", fmt.Errorf("decode OAuth entry: %w", err)
		}
		if token := strings.TrimSpace(entry.AccessToken); token != "" {
			return token, nil
		}
		return "", fmt.Errorf("OAuth accessToken is empty")
	}
	return "", fmt.Errorf("OAuth entry is missing")
}

func parseClaudeOAuthQuota(data []byte) (ClaudeQuota, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(data, &body); err != nil {
		return ClaudeQuota{}, fmt.Errorf("parse Claude usage response: %w", err)
	}
	if body == nil {
		return ClaudeQuota{}, fmt.Errorf("parse Claude usage response: expected object")
	}
	quota := ClaudeQuota{RateLimitsAvailable: true}
	known := make(map[string]bool, len(claudeOAuthKnownWindowIDs))
	for _, id := range claudeOAuthKnownWindowIDs {
		known[id] = true
		if window, ok := parseClaudeOAuthWindow(body[id], false); ok {
			quota.Limits = appendClaudeQuotaLimit(quota.Limits, id, "", window)
		}
	}

	unknownIDs := make([]string, 0, len(body))
	for id := range body {
		if known[id] || id == "extra_usage" || id == "limits" {
			continue
		}
		unknownIDs = append(unknownIDs, id)
	}
	sort.Strings(unknownIDs)
	for _, id := range unknownIDs {
		if window, ok := parseClaudeOAuthWindow(body[id], true); ok {
			quota.Limits = appendClaudeQuotaLimit(quota.Limits, id, "", window)
		}
	}

	if raw, ok := body["extra_usage"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var extra claudeOAuthExtraUsage
		if err := json.Unmarshal(raw, &extra); err == nil {
			quota.ExtraUsage = &ClaudeExtraUsage{
				Enabled:      extra.Enabled,
				UsedPercent:  extra.Utilization,
				MonthlyLimit: extra.MonthlyLimit,
				UsedCredits:  extra.UsedCredits,
				Currency:     strings.TrimSpace(extra.Currency),
			}
		}
	}
	return quota, nil
}

func parseClaudeOAuthWindow(raw json.RawMessage, requireUtilization bool) (*claudeUsageWindow, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var window claudeUsageWindow
	if err := json.Unmarshal(raw, &window); err != nil {
		return nil, false
	}
	if requireUtilization && window.Utilization == nil {
		return nil, false
	}
	if window.Utilization == nil && strings.TrimSpace(window.ResetsAt) == "" {
		return nil, false
	}
	return &window, true
}

type claudeOAuthExtraUsage struct {
	Enabled      bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
	Currency     string   `json:"currency"`
}
