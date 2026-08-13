package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

type temporaryProgressReplyWrapper struct {
	platform.Replier
	progress platform.Replier
}

func (r *temporaryProgressReplyWrapper) ProgressReplier() platform.Replier {
	return r.progress
}

func TestProgressSessionUsesDurableRouteBehindTemporaryReplyWrapper(t *testing.T) {
	h := NewHandler(nil, nil)
	base := newOptionalCapabilityTestReplier()
	base.Caps = platform.Capabilities{Text: true, Streaming: true, FinalReplyOutsideStream: true}
	base.route = platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "bot", ChatID: "chat-1", ReplyToID: "message-1",
	}
	wrapper := &temporaryProgressReplyWrapper{Replier: base, progress: base}
	cfg := config.DefaultProgressConfig()
	cfg.Mode = progressModeStream
	cfg.SendAcceptance = boolPtr(false)
	ctx, cancel := context.WithCancel(context.Background())
	_, _, session := h.startProgressSessionForWorkspaceAgentWithHandle(
		ctx, wrapper, "", "codex", "/workspace/project", "共享任务", cfg,
	)
	t.Cleanup(func() {
		cancel()
		session.stopBackground()
	})
	reporter, ok := optionalDeliveryRouteReporter(session.currentTerminalReply())
	if !ok || reporter.DeliveryRoute() != base.route {
		t.Fatalf("terminal reply route=(%#v,%t), want durable base route %#v", reporter, ok, base.route)
	}
}

type optionalCapabilityTestReplier struct {
	*platformtest.Replier
	clientID         string
	textChunkLimit   int
	remoteMedia      []string
	route            platform.DeliveryRoute
	idempotentText   []string
	idempotentResult []platform.TerminalResult
	deliveryKeys     []string
	checkpoints      []platform.TerminalCheckpoint
	supersedeRefs    []platform.DurableStreamReference
	supersedes       []platform.SupersedeCheckpoint
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

func (r *optionalCapabilityTestReplier) DeliveryRoute() platform.DeliveryRoute {
	return r.route
}

func (r *optionalCapabilityTestReplier) SendTextIdempotent(_ context.Context, text string, deliveryKey string) error {
	r.idempotentText = append(r.idempotentText, text)
	r.deliveryKeys = append(r.deliveryKeys, deliveryKey)
	return nil
}

func (r *optionalCapabilityTestReplier) SendResultIdempotent(_ context.Context, result platform.TerminalResult, deliveryKey string) error {
	r.idempotentResult = append(r.idempotentResult, result)
	r.deliveryKeys = append(r.deliveryKeys, deliveryKey)
	return nil
}

func (r *optionalCapabilityTestReplier) DeliverTerminal(_ context.Context, checkpoint platform.TerminalCheckpoint) error {
	r.checkpoints = append(r.checkpoints, checkpoint)
	return nil
}

func (r *optionalCapabilityTestReplier) PrepareSupersedeFromReference(reference platform.DurableStreamReference, _ string, _ string) (platform.SupersedeCheckpoint, error) {
	r.supersedeRefs = append(r.supersedeRefs, reference)
	return platform.SupersedeCheckpoint{Kind: "feishu-supersede", Payload: []byte(`{"sequence":3}`)}, nil
}

func (r *optionalCapabilityTestReplier) DeliverSupersede(_ context.Context, checkpoint platform.SupersedeCheckpoint) error {
	r.supersedes = append(r.supersedes, checkpoint)
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

func TestSerializedReplierPreservesDurableDeliveryCapabilities(t *testing.T) {
	reply := newOptionalCapabilityTestReplier()
	reply.route = platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "account-1", ChatID: "chat-1", ReplyToID: "message-1",
	}
	serialized := newSerializedReplier(reply)

	reporter, ok := optionalDeliveryRouteReporter(serialized)
	if !ok {
		t.Fatal("serialized replier lost DeliveryRouteReporter")
	}
	if route := reporter.DeliveryRoute(); route != reply.route {
		t.Fatalf("delivery route=%+v, want %+v", route, reply.route)
	}
	idempotent, ok := optionalIdempotentTextReplier(serialized)
	if !ok {
		t.Fatal("serialized replier lost IdempotentTextReplier")
	}
	if err := idempotent.SendTextIdempotent(context.Background(), "done", "delivery-1"); err != nil {
		t.Fatal(err)
	}
	result := platform.TerminalResult{Title: "Codex · weclaw", Text: "### done", State: platform.StreamTerminalCompleted}
	resultSender, ok := optionalIdempotentResultReplier(serialized)
	if !ok {
		t.Fatal("serialized replier lost IdempotentResultReplier")
	}
	if err := resultSender.SendResultIdempotent(context.Background(), result, "delivery-2"); err != nil {
		t.Fatal(err)
	}
	checkpoint := platform.TerminalCheckpoint{Kind: "feishu-card", Payload: []byte(`{"sequence":2}`)}
	durable, ok := optionalDurableTerminalReplier(serialized)
	if !ok {
		t.Fatal("serialized replier lost DurableTerminalReplier")
	}
	if err := durable.DeliverTerminal(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	reference := platform.DurableStreamReference{Kind: "feishu-stream", Payload: []byte(`{"sequence":1}`)}
	preparer, ok := optionalDurableStreamSupersedePreparer(serialized)
	if !ok {
		t.Fatal("serialized replier lost DurableStreamSupersedePreparer")
	}
	supersede, err := preparer.PrepareSupersedeFromReference(reference, "moved", "operation-1")
	if err != nil {
		t.Fatal(err)
	}
	deliverer, ok := optionalDurableSupersedeReplier(serialized)
	if !ok {
		t.Fatal("serialized replier lost DurableSupersedeReplier")
	}
	if err := deliverer.DeliverSupersede(context.Background(), supersede); err != nil {
		t.Fatal(err)
	}

	if len(reply.idempotentText) != 1 || reply.idempotentText[0] != "done" ||
		len(reply.deliveryKeys) != 2 || reply.deliveryKeys[0] != "delivery-1" || reply.deliveryKeys[1] != "delivery-2" {
		t.Fatalf("idempotent delivery: texts=%#v keys=%#v", reply.idempotentText, reply.deliveryKeys)
	}
	if len(reply.idempotentResult) != 1 || reply.idempotentResult[0] != result {
		t.Fatalf("idempotent results=%#v", reply.idempotentResult)
	}
	if len(reply.checkpoints) != 1 || reply.checkpoints[0].Kind != checkpoint.Kind ||
		string(reply.checkpoints[0].Payload) != string(checkpoint.Payload) {
		t.Fatalf("checkpoints=%#v", reply.checkpoints)
	}
	if len(reply.supersedeRefs) != 1 || reply.supersedeRefs[0].Kind != reference.Kind ||
		len(reply.supersedes) != 1 || reply.supersedes[0].Kind != supersede.Kind {
		t.Fatalf("supersede refs=%#v checkpoints=%#v", reply.supersedeRefs, reply.supersedes)
	}
}
