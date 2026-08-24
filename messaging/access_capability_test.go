package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type accessCapabilityTestPlatform struct {
	name    platform.PlatformName
	account string
}

func (p *accessCapabilityTestPlatform) Name() platform.PlatformName { return p.name }
func (p *accessCapabilityTestPlatform) AccountID() string           { return p.account }
func (p *accessCapabilityTestPlatform) Capabilities() platform.Capabilities {
	return platform.Capabilities{Text: true}
}
func (p *accessCapabilityTestPlatform) Run(context.Context, platform.DispatchFunc) error { return nil }

func authorizeIncomingMessageForTest(t *testing.T, msg platform.IncomingMessage, allowed ...string) platform.IncomingMessage {
	t.Helper()
	if msg.Platform == "" {
		msg.Platform = platform.PlatformWeChat
	}
	if msg.AccountID == "" {
		msg.AccountID = "test-account"
	}
	if msg.Platform == platform.PlatformFeishu && msg.Metadata != nil {
		if unionID := msg.Metadata["feishu_union_id"]; unionID != "" {
			msg.UserAliases = append(msg.UserAliases, unionID)
		}
	}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: &accessCapabilityTestPlatform{name: msg.Platform, account: msg.AccountID},
		Access:   platform.NewAccessControl(allowed),
	}})
	authorized, ok := registry.AuthorizeIncomingMessage(msg)
	if !ok {
		t.Fatalf("failed to authorize test message platform=%s account=%s identities=%v allowed=%v",
			msg.Platform, msg.AccountID, msg.UserIdentityKeys(), allowed)
	}
	return authorized
}

// handleMessageForTest 为普通业务测试补齐生产路径要求的 Registry grant。
// 授权缺失或 grant 失效的安全边界测试必须直接调用 HandleMessage。
func (h *Handler) handleMessageForTest(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
	if !msg.HasAuthorizedAccess() {
		if msg.Platform == "" {
			msg.Platform = platform.PlatformWeChat
		}
		if msg.AccountID == "" {
			msg.AccountID = "test-account"
		}
		if msg.Platform == platform.PlatformFeishu && msg.Metadata != nil {
			if unionID := msg.Metadata["feishu_union_id"]; unionID != "" {
				msg.UserAliases = append(msg.UserAliases, unionID)
			}
		}
		allowed := msg.UserIdentityKeys()
		if len(allowed) > 0 {
			registry := platform.NewRegistry([]platform.RegistryEntry{{
				Platform: &accessCapabilityTestPlatform{name: msg.Platform, account: msg.AccountID},
				Access:   platform.NewAccessControl(allowed),
			}})
			if authorized, ok := registry.AuthorizeIncomingMessage(msg); ok {
				msg = authorized
			}
		}
	}
	h.HandleMessage(ctx, msg, reply)
}

func TestHandleMessageRejectsMissingRegistryAccessGrant(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})

	h.HandleMessage(context.Background(), platform.IncomingMessage{
		Platform:  platform.PlatformWeChat,
		AccountID: "personal",
		UserID:    "untrusted",
		Text:      "hello",
	}, reply)

	if texts := reply.TextsSnapshot(); len(texts) != 0 {
		t.Fatalf("message without Registry access grant produced replies: %#v", texts)
	}
}
