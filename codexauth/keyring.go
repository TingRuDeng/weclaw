package codexauth

import (
	"errors"

	keyring "github.com/zalando/go-keyring"
)

const keyringService = "weclaw.codex.oauth"

var errSecretNotFound = errors.New("secret not found")

// SecretStore 是 OAuth 快照后端的最小能力；实现不得把 secret 写入日志或索引。
type SecretStore interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

// KeyringClient 保留对系统凭据库测试桩的直观命名。
type KeyringClient = SecretStore

type systemKeyringClient struct{}

func (systemKeyringClient) Get(service, user string) (string, error) {
	value, err := keyring.Get(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errSecretNotFound
	}
	return value, err
}

func (systemKeyringClient) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (systemKeyringClient) Delete(service, user string) error {
	err := keyring.Delete(service, user)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
