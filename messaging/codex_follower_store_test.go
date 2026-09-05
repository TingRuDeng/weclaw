package messaging

import (
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type codexFollowerRouteReplier struct {
	*platformtest.Replier
	route platform.DeliveryRoute
}

func (r *codexFollowerRouteReplier) DeliveryRoute() platform.DeliveryRoute {
	return r.route
}

type codexFollowerProgressWrapper struct {
	platform.Replier
}

func (r *codexFollowerProgressWrapper) ProgressReplier() platform.Replier {
	return r.Replier
}

func TestCodexFollowerFromAcquireUnwrapsCardProgressRepliers(t *testing.T) {
	route := platform.DeliveryRoute{
		Platform:  platform.PlatformFeishu,
		AccountID: "bot-a",
		ChatID:    "chat-a",
		ReplyToID: "message-a",
	}
	base := &codexFollowerRouteReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}),
		route:   route,
	}
	deferred := &codexFollowerProgressWrapper{Replier: base}
	inline := &codexFollowerProgressWrapper{Replier: deferred}

	follower, ok := codexFollowerFromAcquire(codexSessionAcquireRequest{
		actorUserID: "user-a", authorizedIdentity: "union-a",
		platform:  platform.PlatformFeishu,
		accountID: "bot-a",
		reply:     inline,
		route: codexConversationRoute{
			workspaceRoot: "/workspace/project-a",
			threadID:      "thread-a",
		},
	})

	if !ok || follower == nil {
		t.Fatal("card callback replier chain did not produce a durable follower")
	}
	if follower.DeliveryRoute != route {
		t.Fatalf("delivery route = %#v, want %#v", follower.DeliveryRoute, route)
	}
	if follower.AuthorizedIdentity != "union-a" {
		t.Fatalf("authorized identity = %q, want union-a", follower.AuthorizedIdentity)
	}
}

func TestCodexFollowerFromAcquireRequiresAuthorizedIdentity(t *testing.T) {
	route := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot-a", ChatID: "chat-a", ReplyToID: "message-a",
	}
	base := &codexFollowerRouteReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true}),
		route:   route,
	}

	follower, ok := codexFollowerFromAcquire(codexSessionAcquireRequest{
		actorUserID: "user-a", platform: platform.PlatformFeishu, accountID: "bot-a", reply: base,
		route: codexConversationRoute{workspaceRoot: "/workspace/project-a", threadID: "thread-a"},
	})

	if ok || follower != nil {
		t.Fatalf("missing authorized identity created follower=%#v ok=%v", follower, ok)
	}
}

func TestCodexFollowerFromAcquirePersistsWechatRoute(t *testing.T) {
	route := platform.DeliveryRoute{Platform: platform.PlatformWeChat, AccountID: "wechat-account", ChatID: "wechat-user"}
	replier := &codexFollowerRouteReplier{Replier: platformtest.NewReplier(platform.Capabilities{Text: true}), route: route}
	request := codexSessionAcquireRequest{
		actorUserID: "wechat-user", authorizedIdentity: "wechat-user", platform: platform.PlatformWeChat,
		accountID: "wechat-account", reply: replier,
		route: codexConversationRoute{workspaceRoot: "/workspace/project", threadID: "original-thread"},
	}
	follower, ok := codexFollowerFromAcquire(request)
	if !ok || follower == nil || follower.DeliveryRoute != route || follower.AuthorizedIdentity != "wechat-user" {
		t.Fatalf("WeChat route was not retained: follower=%#v ok=%v", follower, ok)
	}
	request.platform = platform.PlatformFeishu
	if follower, ok := codexFollowerFromAcquire(request); ok || follower != nil {
		t.Fatal("accepted a delivery route from a different platform")
	}
}
