package codexauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type authFile struct {
	AuthMode     string          `json:"auth_mode"`
	OpenAIAPIKey json.RawMessage `json:"OPENAI_API_KEY"`
	Tokens       struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// Snapshot 保存经过校验的完整 auth.json。原始凭据不通过 JSON 或日志接口暴露。
type Snapshot struct {
	raw                []byte
	authMode           string
	accountFingerprint string
	email              string
	emailMasked        string
	emailFingerprint   string
}

// MarshalJSON 只允许输出脱敏视图；完整认证正文只能通过受控的 Bytes 写入
// SecretStore 或 auth.json，不能被 API/日志的通用 JSON 编码意外带出。
func (s Snapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AuthMode    string `json:"auth_mode"`
		EmailMasked string `json:"email_masked,omitempty"`
	}{
		AuthMode: s.authMode, EmailMasked: s.emailMasked,
	})
}

func ParseSnapshot(data []byte) (*Snapshot, error) {
	var parsed authFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&parsed); err != nil {
		return nil, NewError(CodeInvalid, "Codex 认证文件不是有效 JSON", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, NewError(CodeInvalid, "Codex 认证文件包含多余内容", nil)
	}
	if strings.TrimSpace(parsed.AuthMode) != "chatgpt" {
		return nil, NewError(CodeUnsupportedAuth, "仅支持 Codex ChatGPT OAuth 账号", nil)
	}
	if hasNonEmptyAPIKey(parsed.OpenAIAPIKey) {
		return nil, NewError(CodeUnsupportedAuth, "Codex ChatGPT OAuth 认证中不能同时保存 API Key、PAT 或 Bedrock 凭据", nil)
	}
	missing := make([]string, 0, 4)
	if strings.TrimSpace(parsed.Tokens.AccessToken) == "" {
		missing = append(missing, "access_token")
	}
	if strings.TrimSpace(parsed.Tokens.RefreshToken) == "" {
		missing = append(missing, "refresh_token")
	}
	if strings.TrimSpace(parsed.Tokens.IDToken) == "" {
		missing = append(missing, "id_token")
	}
	if strings.TrimSpace(parsed.Tokens.AccountID) == "" {
		missing = append(missing, "account_id")
	}
	if len(missing) > 0 {
		return nil, NewError(CodeUnsupportedAuth, "Codex OAuth 认证缺少必要字段: "+strings.Join(missing, ", "), nil)
	}
	email, err := emailFromIDToken(parsed.Tokens.IDToken)
	if err != nil {
		return nil, NewError(CodeUnsupportedAuth, "Codex OAuth id_token 缺少可验证账号邮箱", err)
	}
	raw := append([]byte(nil), data...)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		raw = append(raw, '\n')
	}
	return &Snapshot{
		raw:                raw,
		authMode:           "chatgpt",
		accountFingerprint: fingerprint(parsed.Tokens.AccountID),
		email:              strings.ToLower(strings.TrimSpace(email)),
		emailMasked:        maskEmail(email),
		emailFingerprint:   fingerprint(strings.ToLower(strings.TrimSpace(email))),
	}, nil
}

func hasNonEmptyAPIKey(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text) != ""
	}
	return true
}

func (s *Snapshot) Bytes() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.raw...)
}

func (s *Snapshot) AuthMode() string           { return s.authMode }
func (s *Snapshot) AccountFingerprint() string { return s.accountFingerprint }
func (s *Snapshot) EmailMasked() string        { return s.emailMasked }
func (s *Snapshot) EmailFingerprint() string   { return s.emailFingerprint }
func (s *Snapshot) MatchesEmail(email string) bool {
	return s != nil && s.emailFingerprint != "" && EmailFingerprint(email) == s.emailFingerprint
}

// EmailFingerprint 返回与 profile 索引相同的脱敏邮箱指纹，供账户运行态
// 一致性校验使用；调用方不得据此输出原始邮箱。
func EmailFingerprint(email string) string {
	normalized := strings.ToLower(strings.TrimSpace(email))
	if normalized == "" {
		return ""
	}
	return fingerprint(normalized)
}

func emailFromIDToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode JWT claims: %w", err)
	}
	if strings.TrimSpace(claims.Email) == "" {
		return "", fmt.Errorf("email claim missing")
	}
	return claims.Email, nil
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:16]
}

func maskEmail(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" {
		return "***"
	}
	visible := local[:1]
	if len(local) > 2 {
		visible += "***" + local[len(local)-1:]
	} else {
		visible += "***"
	}
	return visible + "@" + domain
}
