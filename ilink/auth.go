package ilink

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/internal/accountstore"
	"github.com/fastclaw-ai/weclaw/internal/securefile"
)

const (
	qrCodeURL       = "https://ilinkai.weixin.qq.com/ilink/bot/get_bot_qrcode?bot_type=3"
	qrStatusURL     = "https://ilinkai.weixin.qq.com/ilink/bot/get_qrcode_status?qrcode="
	statusWait      = "wait"
	statusScanned   = "scaned"
	statusConfirmed = "confirmed"
	statusExpired   = "expired"

	accountStoreWriteTimeout = 5 * time.Second
	qrTransportErrorBackoff  = time.Second
)

type qrStatusClient interface {
	doGet(context.Context, string, any) error
}

// FetchQRCode retrieves a new QR code for login.
func FetchQRCode(ctx context.Context) (*QRCodeResponse, error) {
	c := NewUnauthenticatedClient()
	var resp QRCodeResponse
	if err := c.doGet(ctx, qrCodeURL, &resp); err != nil {
		return nil, fmt.Errorf("fetch QR code: %w", err)
	}
	return &resp, nil
}

// PollQRStatus polls for QR code scan status until confirmed or expired.
// It calls onStatus for each status change so the caller can display progress.
func PollQRStatus(ctx context.Context, qrcode string, onStatus func(status string)) (*Credentials, error) {
	c := NewUnauthenticatedClient()
	url := qrStatusURL + qrcode
	return pollQRStatus(ctx, c, url, onStatus)
}

func pollQRStatus(ctx context.Context, c qrStatusClient, url string, onStatus func(status string)) (*Credentials, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pollCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		var resp QRStatusResponse
		err := c.doGet(pollCtx, url, &resp)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// 长轮询超时和即时传输错误都要进入可取消退避，避免网络快速失败时忙循环。
			if err := waitForRetry(ctx, qrTransportErrorBackoff); err != nil {
				return nil, err
			}
			continue
		}

		if onStatus != nil {
			onStatus(resp.Status)
		}

		switch resp.Status {
		case statusConfirmed:
			creds := &Credentials{
				BotToken:    resp.BotToken,
				ILinkBotID:  resp.ILinkBotID,
				BaseURL:     resp.BaseURL,
				ILinkUserID: resp.ILinkUserID,
			}
			return creds, nil
		case statusExpired:
			return nil, fmt.Errorf("QR code expired")
		case statusWait, statusScanned:
			// Continue polling
		default:
			// Unknown status, continue
		}
	}
}

// AccountsDir returns the directory where account credentials are stored.
func AccountsDir() (string, error) {
	home, err := config.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "accounts"), nil
}

// NormalizeAccountID converts raw bot ID to filesystem-safe format.
func NormalizeAccountID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	const hexDigits = "0123456789abcdef"
	var builder strings.Builder
	encodedUnsafe := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_':
			builder.WriteByte(ch)
		case ch == '@', ch == '.', ch == ':':
			// 保持既有正常账号文件名兼容。
			builder.WriteByte('-')
		default:
			encodedUnsafe = true
			builder.WriteByte('_')
			builder.WriteByte(hexDigits[ch>>4])
			builder.WriteByte(hexDigits[ch&0x0f])
		}
	}
	result := builder.String()
	if result == "" {
		return ""
	}
	if encodedUnsafe || len(result) > 120 {
		sum := sha256.Sum256([]byte(raw))
		if len(result) > 96 {
			result = result[:96]
		}
		result += fmt.Sprintf("-%x", sum[:8])
	}
	return result
}

// SaveCredentials saves credentials to disk under ~/.weclaw/accounts/{id}.json.
func SaveCredentials(creds *Credentials) error {
	ctx, cancel := context.WithTimeout(context.Background(), accountStoreWriteTimeout)
	defer cancel()
	return saveCredentials(ctx, creds)
}

func saveCredentials(ctx context.Context, creds *Credentials) error {
	if creds == nil {
		return fmt.Errorf("save credentials: nil credentials")
	}
	dir, err := AccountsDir()
	if err != nil {
		return err
	}
	return accountstore.WithWriteLock(ctx, dir, func() error {
		id := NormalizeAccountID(creds.ILinkBotID)
		if id == "" {
			return fmt.Errorf("save credentials: empty bot id")
		}
		path := filepath.Join(dir, id+".json")
		if existingData, readErr := securefile.ReadForUpdate(path); readErr == nil {
			var existing Credentials
			if json.Unmarshal(existingData, &existing) == nil &&
				strings.TrimSpace(existing.ILinkBotID) != "" &&
				strings.TrimSpace(existing.ILinkBotID) != strings.TrimSpace(creds.ILinkBotID) {
				return fmt.Errorf("save credentials: bot id filename collision for %q", id)
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("read existing credentials: %w", readErr)
		}

		data, err := json.MarshalIndent(creds, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal credentials: %w", err)
		}

		if err := securefile.Write(path, data); err != nil {
			return fmt.Errorf("write credentials: %w", err)
		}
		return nil
	})
}

// LoadAllCredentials loads all saved account credentials.
func LoadAllCredentials() ([]*Credentials, error) {
	dir, err := AccountsDir()
	if err != nil {
		return nil, err
	}

	if err := securefile.ValidateDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("validate accounts dir: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read accounts dir: %w", err)
	}

	var result []*Credentials
	loadedIDs := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := securefile.Read(filepath.Join(dir, e.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read credentials %q: %w", e.Name(), err)
		}
		var creds Credentials
		if json.Unmarshal(data, &creds) == nil && strings.TrimSpace(creds.BotToken) != "" {
			id := NormalizeAccountID(creds.ILinkBotID)
			if id == "" {
				continue
			}
			rawID := strings.TrimSpace(creds.ILinkBotID)
			if previous, exists := loadedIDs[id]; exists {
				if previous != rawID {
					return nil, fmt.Errorf("load credentials: multiple bot IDs map to %q", id)
				}
				continue
			}
			loadedIDs[id] = rawID
			result = append(result, &creds)
		}
	}
	return result, nil
}

// CredentialsPath returns the path for display purposes.
func CredentialsPath() (string, error) {
	return AccountsDir()
}
