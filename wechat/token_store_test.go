package wechat

import (
	"os"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestContextTokenStorePersistsAndLoads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newContextTokenStore("bot-1")

	if err := store.Set("user-1", "ctx-1"); err != nil {
		t.Fatalf("Set token error: %v", err)
	}
	loaded := newContextTokenStore("bot-1")

	if got := loaded.Get("user-1"); got != "ctx-1" {
		t.Fatalf("loaded token=%q, want ctx-1", got)
	}
	data, err := os.ReadFile(contextTokenStorePath("bot-1"))
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	if !strings.Contains(string(data), "wechat:user-1") {
		t.Fatalf("token file=%s, want platform-qualified key", string(data))
	}
}

func TestAdapterNewReplierUsesPersistedContextToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	adapter := NewAdapter(&ilink.Credentials{ILinkBotID: "bot-1"})
	if err := adapter.tokenStore.Set("user-1", "ctx-1"); err != nil {
		t.Fatalf("Set token error: %v", err)
	}

	reply := adapter.NewReplier("user-1")
	wechatReply, ok := reply.(*Replier)
	if !ok {
		t.Fatalf("reply=%T, want *Replier", reply)
	}
	if wechatReply.ContextToken != "ctx-1" {
		t.Fatalf("ContextToken=%q, want persisted token", wechatReply.ContextToken)
	}
}
