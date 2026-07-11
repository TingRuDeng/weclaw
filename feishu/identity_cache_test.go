package feishu

import (
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/platform"
)

func TestIdentityCacheExpiresInactiveUsers(t *testing.T) {
	now := time.Unix(100, 0)
	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.now = func() time.Time { return now }
	adapter.rememberUserIdentities(platform.IncomingMessage{
		UserID:      "ou_user",
		UserAliases: []string{"on_union"},
	})
	if got := adapter.identityKeysForUser("ou_user"); len(got) != 2 {
		t.Fatalf("cached identities=%#v, want open_id and union_id", got)
	}

	now = now.Add(feishuIdentityCacheTTL + time.Second)
	got := adapter.identityKeysForUser("ou_user")
	if len(got) != 1 || got[0] != "ou_user" {
		t.Fatalf("expired identities=%#v, want user id fallback", got)
	}
}
