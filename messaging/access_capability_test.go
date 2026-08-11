package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
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
