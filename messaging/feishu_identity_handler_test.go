package messaging

import (
	"path/filepath"
	"testing"
)

func TestHandlerObservesFeishuIdentity(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "feishu-identities.json")
	handler := NewHandler(nil, nil)
	handler.SetFeishuIdentityFile(stateFile)

	handler.ObserveFeishuIdentity(feishuIdentityMessage("cli_a", "ou_a", "user_a", "on_same_person"))

	restored := newFeishuIdentityStore()
	restored.SetFilePath(stateFile)
	pending := restored.ListPending()
	if len(pending) != 1 || pending[0].Key != "on_same_person" {
		t.Fatalf("pending=%#v, want observed feishu identity", pending)
	}
}
