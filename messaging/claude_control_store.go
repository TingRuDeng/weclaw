package messaging

import (
	"strings"
	"time"
)

const claudeSessionStateVersion = 3

type claudeControlOwner string

const (
	claudeOwnerUnclaimed claudeControlOwner = "unclaimed"
	claudeOwnerLocal     claudeControlOwner = "local"
	claudeOwnerRemote    claudeControlOwner = "remote"
)

type claudeControlIntent struct {
	Owner          claudeControlOwner `json:"owner"`
	BindingKey     string             `json:"binding_key,omitempty"`
	ConversationID string             `json:"conversation_id,omitempty"`
	Revision       uint64             `json:"revision"`
	UpdatedAt      string             `json:"updated_at"`
}

func normalizeClaudeControlIntent(intent claudeControlIntent) claudeControlIntent {
	intent.BindingKey = strings.TrimSpace(intent.BindingKey)
	intent.ConversationID = strings.TrimSpace(intent.ConversationID)
	switch intent.Owner {
	case claudeOwnerRemote:
		if intent.BindingKey == "" || intent.ConversationID == "" {
			return claudeControlIntent{
				Owner: claudeOwnerUnclaimed, Revision: intent.Revision, UpdatedAt: intent.UpdatedAt,
			}
		}
	case claudeOwnerLocal, claudeOwnerUnclaimed:
		intent.BindingKey = ""
		intent.ConversationID = ""
	default:
		intent = claudeControlIntent{
			Owner: claudeOwnerUnclaimed, Revision: intent.Revision, UpdatedAt: intent.UpdatedAt,
		}
	}
	return intent
}

func newMigratedClaudeControl(owner claudeControlOwner, key string, conversationID string, updatedAt string) claudeControlIntent {
	if strings.TrimSpace(updatedAt) == "" {
		updatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	return normalizeClaudeControlIntent(claudeControlIntent{
		Owner: owner, BindingKey: key, ConversationID: conversationID, Revision: 1, UpdatedAt: updatedAt,
	})
}

func cloneClaudeControls(input map[string]claudeControlIntent) map[string]claudeControlIntent {
	controls := make(map[string]claudeControlIntent, len(input))
	for sessionID, intent := range input {
		controls[sessionID] = intent
	}
	return controls
}
