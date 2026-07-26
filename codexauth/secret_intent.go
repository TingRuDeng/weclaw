package codexauth

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/uuid"
)

const secretCreateIntentVersion = 1

type secretCreateIntent struct {
	Backend SecretBackend `json:"backend"`
	Ref     string        `json:"ref"`
}

type secretCreateIntentState struct {
	Version int                  `json:"version"`
	Creates []secretCreateIntent `json:"creates"`
}

func (s *Store) readSecretCreateIntents() ([]pendingSecret, error) {
	data, err := readSecureFile(s.intentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, wrapInvalid("读取凭据创建意图", err)
	}
	var state secretCreateIntentState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, wrapInvalid("解析凭据创建意图", err)
	}
	if state.Version != secretCreateIntentVersion {
		return nil, wrapInvalid("读取凭据创建意图", fmt.Errorf("unsupported intent version %d", state.Version))
	}
	intents := make([]pendingSecret, 0, len(state.Creates))
	seen := make(map[string]struct{}, len(state.Creates))
	for _, intent := range state.Creates {
		if err := validateSecretReference(intent.Backend, intent.Ref); err != nil {
			return nil, wrapInvalid("校验凭据创建意图", err)
		}
		key := secretReferenceKey(intent.Backend, intent.Ref)
		if _, exists := seen[key]; exists {
			return nil, wrapInvalid("校验凭据创建意图", fmt.Errorf("duplicate secret create intent"))
		}
		seen[key] = struct{}{}
		intents = append(intents, pendingSecret{backend: intent.Backend, ref: intent.Ref})
	}
	return intents, nil
}

func (s *Store) writeSecretCreateIntents(intents []pendingSecret) error {
	state := secretCreateIntentState{
		Version: secretCreateIntentVersion,
		Creates: make([]secretCreateIntent, 0, len(intents)),
	}
	for _, intent := range intents {
		if err := validateSecretReference(intent.backend, intent.ref); err != nil {
			return wrapInvalid("保存凭据创建意图", err)
		}
		state.Creates = append(state.Creates, secretCreateIntent{
			Backend: intent.backend,
			Ref:     intent.ref,
		})
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return wrapInvalid("序列化凭据创建意图", err)
	}
	data = append(data, '\n')
	if err := atomicWriteSecureFile(s.intentPath, data); err != nil {
		return wrapInvalid("保存凭据创建意图", err)
	}
	return nil
}

func validateSecretReference(backend SecretBackend, ref string) error {
	if backend != SecretBackendKeyring && backend != SecretBackendFile {
		return fmt.Errorf("unsupported secret backend")
	}
	if _, err := uuid.Parse(ref); err != nil {
		return fmt.Errorf("invalid secret reference")
	}
	return nil
}

func secretReferenceKey(backend SecretBackend, ref string) string {
	return string(backend) + "\x00" + ref
}
