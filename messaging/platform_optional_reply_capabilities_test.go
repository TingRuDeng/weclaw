package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type optionalCapabilityTestReplier struct {
	*platformtest.Replier
	clientID       string
	textChunkLimit int
	remoteMedia    []string
}

func newOptionalCapabilityTestReplier() *optionalCapabilityTestReplier {
	return &optionalCapabilityTestReplier{
		Replier: platformtest.NewReplier(platform.Capabilities{Text: true, Image: true}),
	}
}

func (r *optionalCapabilityTestReplier) SetClientID(clientID string) {
	r.clientID = clientID
}

func (r *optionalCapabilityTestReplier) SetTextChunkLimit(maxRunes int) {
	r.textChunkLimit = maxRunes
}

func (r *optionalCapabilityTestReplier) SendMediaFromURL(_ context.Context, mediaURL string) error {
	r.remoteMedia = append(r.remoteMedia, mediaURL)
	return nil
}

func TestPreparePlatformMessageSetsClientIDThroughOptionalCapability(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newOptionalCapabilityTestReplier()
	runtime, ready := h.preparePlatformMessage(platformMessageRuntime{
		ctx: context.Background(),
		msg: platform.IncomingMessage{
			Platform: platform.PlatformWeChat,
			UserID:   "user-1",
			Text:     "hello",
		},
		reply:       reply,
		routeUserID: "user-1",
		text:        "hello",
	})

	if !ready {
		t.Fatal("message should be ready")
	}
	if reply.clientID == "" || reply.clientID != runtime.clientID {
		t.Fatalf("reply clientID=%q, runtime clientID=%q", reply.clientID, runtime.clientID)
	}
}

func TestSendReplyProjectionUsesOptionalAdapterCapabilitiesThroughSerializedReplier(t *testing.T) {
	h := NewHandler(nil, nil)
	reply := newOptionalCapabilityTestReplier()
	serialized := newSerializedReplier(reply)
	ctx := withTextReplyChunkLimit(context.Background(), 37)

	h.sendReplyProjection(replyDeliveryRequest{
		ctx: ctx, replyWriter: serialized, userID: "user-1",
	}, replyDeliveryProjection{
		text:      "done",
		imageURLs: []string{"https://example.com/image.png"},
	}, false)

	if reply.textChunkLimit != 37 {
		t.Fatalf("text chunk limit=%d, want 37", reply.textChunkLimit)
	}
	if len(reply.remoteMedia) != 1 || reply.remoteMedia[0] != "https://example.com/image.png" {
		t.Fatalf("remote media=%#v", reply.remoteMedia)
	}
	if len(reply.Texts) != 1 || reply.Texts[0] != "done" {
		t.Fatalf("texts=%#v", reply.Texts)
	}
}
