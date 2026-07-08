package platform

import (
	"context"
	"testing"
)

func TestRegistryDispatchesAllowedUser(t *testing.T) {
	reply := &recordingReplier{}
	platform := &recordingPlatform{messages: []IncomingMessage{{Platform: PlatformWeChat, UserID: "user-1", Text: "hi"}}}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl([]string{"user-1"})}})
	var got []IncomingMessage

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		got = append(got, msg)
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "user-1" {
		t.Fatalf("got messages=%#v, want user-1", got)
	}
	_ = reply
}

func TestRegistryAllowsFeishuByOriginalUserIDWhenSessionMetadataExists(t *testing.T) {
	platform := &recordingPlatform{
		name: PlatformFeishu,
		messages: []IncomingMessage{{
			Platform: PlatformFeishu,
			UserID:   "ou_user",
			Text:     "hi",
			Metadata: map[string]string{"feishu_session_key": "feishu:tenant_1:group:oc_1:om_root"},
		}},
	}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl([]string{"ou_user"})}})
	var got []IncomingMessage

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		got = append(got, msg)
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "ou_user" {
		t.Fatalf("got messages=%#v, want original Feishu user", got)
	}
}

func TestRegistryDispatchesAllowedFeishuUserAlias(t *testing.T) {
	platform := &recordingPlatform{
		name: PlatformFeishu,
		messages: []IncomingMessage{{
			Platform:    PlatformFeishu,
			UserID:      "ou_android_user",
			UserAliases: []string{"on_same_person"},
			Text:        "hi",
		}},
	}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl([]string{"on_same_person"})}})
	var got []IncomingMessage

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		got = append(got, msg)
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(got) != 1 || got[0].UserID != "ou_android_user" {
		t.Fatalf("got messages=%#v, want alias-authorized Feishu user", got)
	}
}

func TestRegistryRejectsEmptyAllowlistByDefault(t *testing.T) {
	reply := &recordingReplier{}
	platform := &recordingPlatform{
		messages: []IncomingMessage{{Platform: PlatformWeChat, UserID: "user-1", Text: "hi"}},
		reply:    reply,
	}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl(nil)}})
	called := false

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		called = true
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if called {
		t.Fatalf("dispatch should not be called for empty allowlist")
	}
	if len(reply.texts) != 1 || reply.texts[0] != denyNoticeText {
		t.Fatalf("deny notice texts=%#v, want one safe notice", reply.texts)
	}
}

func TestRegistryObservesDeniedFeishuIdentity(t *testing.T) {
	reply := &recordingReplier{}
	msg := IncomingMessage{
		Platform:    PlatformFeishu,
		AccountID:   "cli_a",
		UserID:      "ou_user",
		UserAliases: []string{"on_same_person"},
		Text:        "hi",
	}
	platform := &recordingPlatform{
		name:     PlatformFeishu,
		messages: []IncomingMessage{msg},
		reply:    reply,
	}
	var observed []IncomingMessage
	registry := NewRegistry(
		[]RegistryEntry{{Platform: platform, Access: NewAccessControl(nil)}},
		WithIdentityObserver(func(msg IncomingMessage) {
			observed = append(observed, msg)
		}),
	)
	called := false

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		called = true
	})

	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if called {
		t.Fatal("dispatch should remain denied")
	}
	if len(observed) != 1 || observed[0].UserID != "ou_user" {
		t.Fatalf("observed=%#v, want denied feishu identity", observed)
	}
}

func TestRegistryUsesCustomDenyNotice(t *testing.T) {
	reply := &recordingReplier{}
	msg := IncomingMessage{Platform: PlatformFeishu, AccountID: "cli_a", UserID: "ou_user", Text: "hi"}
	platform := &recordingPlatform{name: PlatformFeishu, messages: []IncomingMessage{msg}, reply: reply}
	registry := NewRegistry(
		[]RegistryEntry{{Platform: platform, Access: NewAccessControl(nil)}},
		WithDenyNoticeProvider(func(IncomingMessage) string {
			return "当前账号无权限，请联系管理员授权。\n授权码: 123456"
		}),
	)

	err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {
		t.Fatal("dispatch should not be called for denied user")
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(reply.texts) != 1 || reply.texts[0] != "当前账号无权限，请联系管理员授权。\n授权码: 123456" {
		t.Fatalf("deny notice texts=%#v, want custom auth code notice", reply.texts)
	}
}

func TestRegistryRateLimitsDenyNotice(t *testing.T) {
	reply := &recordingReplier{}
	platform := &recordingPlatform{
		messages: []IncomingMessage{
			{Platform: PlatformWeChat, UserID: "user-1", Text: "hi"},
			{Platform: PlatformWeChat, UserID: "user-1", Text: "again"},
		},
		reply: reply,
	}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl(nil)}})

	if err := registry.Run(context.Background(), func(ctx context.Context, msg IncomingMessage, reply Replier) {}); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if len(reply.texts) != 1 {
		t.Fatalf("deny notice count=%d, want rate limited to one", len(reply.texts))
	}
}

func TestRegistryInjectsAccessControlIntoPlatform(t *testing.T) {
	platform := &accessAwareRecordingPlatform{}
	registry := NewRegistry([]RegistryEntry{{Platform: platform, Access: NewAccessControl([]string{"user-1"})}})

	if !platform.access.Allowed("user-1") {
		t.Fatalf("platform access should allow initial user")
	}
	registry.UpdateAccess(PlatformWeChat, []string{"user-2"})
	if platform.access.Allowed("user-1") || !platform.access.Allowed("user-2") {
		t.Fatalf("platform access not updated after hot reload")
	}
}

func TestRegistryUpdatesAccessForSpecificAccount(t *testing.T) {
	first := &accessAwareRecordingPlatform{recordingPlatform: recordingPlatform{name: PlatformFeishu, accountID: "cli_a"}}
	second := &accessAwareRecordingPlatform{recordingPlatform: recordingPlatform{name: PlatformFeishu, accountID: "cli_b"}}
	registry := NewRegistry([]RegistryEntry{
		{Platform: first, Access: NewAccessControl([]string{"ou_a"})},
		{Platform: second, Access: NewAccessControl([]string{"ou_b"})},
	})

	registry.UpdateAccessForAccount(PlatformFeishu, "cli_b", []string{"ou_c"})

	if !first.access.Allowed("ou_a") || first.access.Allowed("ou_c") {
		t.Fatalf("first access was changed unexpectedly")
	}
	if second.access.Allowed("ou_b") || !second.access.Allowed("ou_c") {
		t.Fatalf("second access not updated for target account")
	}
}

func TestRegistryHasAccount(t *testing.T) {
	first := &recordingPlatform{name: PlatformFeishu, accountID: "cli_a"}
	second := &recordingPlatform{name: PlatformFeishu, accountID: "cli_b"}
	registry := NewRegistry([]RegistryEntry{
		{Platform: first, Access: NewAccessControl([]string{"ou_a"})},
		{Platform: second, Access: NewAccessControl([]string{"ou_b"})},
	})

	if !registry.HasAccount(PlatformFeishu, "cli_b") {
		t.Fatal("registry should report existing feishu account")
	}
	if registry.HasAccount(PlatformFeishu, "cli_missing") {
		t.Fatal("registry should not report missing feishu account")
	}
}

type recordingPlatform struct {
	name      PlatformName
	accountID string
	messages  []IncomingMessage
	err       error
	reply     Replier
}

type accessAwareRecordingPlatform struct {
	recordingPlatform
	access AccessControl
}

func (p *accessAwareRecordingPlatform) SetAccessControl(access AccessControl) {
	p.access = access
}

func (p *recordingPlatform) Name() PlatformName {
	if p.name != "" {
		return p.name
	}
	return PlatformWeChat
}

func (p *recordingPlatform) AccountID() string {
	if p.accountID != "" {
		return p.accountID
	}
	return "acct-1"
}

func (p *recordingPlatform) Capabilities() Capabilities {
	return Capabilities{Text: true}
}

func (p *recordingPlatform) Run(ctx context.Context, dispatch DispatchFunc) error {
	for _, msg := range p.messages {
		reply := p.reply
		if reply == nil {
			reply = &recordingReplier{}
		}
		dispatch(ctx, msg, reply)
	}
	return p.err
}

type recordingReplier struct {
	texts []string
}

func (r *recordingReplier) Capabilities() Capabilities { return Capabilities{Text: true} }
func (r *recordingReplier) SendText(ctx context.Context, text string) error {
	r.texts = append(r.texts, text)
	return nil
}
func (r *recordingReplier) SendImage(ctx context.Context, localPath string) error {
	return nil
}
func (r *recordingReplier) SendFile(ctx context.Context, localPath string) error {
	return nil
}
func (r *recordingReplier) Typing(ctx context.Context, on bool) error { return nil }
func (r *recordingReplier) OpenStream(ctx context.Context, opts StreamOptions) (Stream, error) {
	return nil, ErrUnsupported
}
func (r *recordingReplier) AskChoices(ctx context.Context, prompt string, choices []Choice) error {
	return nil
}
