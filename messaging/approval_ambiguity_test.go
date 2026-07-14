package messaging

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/platform"
	"github.com/fastclaw-ai/weclaw/platform/platformtest"
)

// TestAmbiguousApprovalTextPromptsForCardSelection 验证文本同时匹配多个审批时不会进入普通消息流程。
func TestAmbiguousApprovalTextPromptsForCardSelection(t *testing.T) {
	h := NewHandler(nil, nil)
	options := []agent.ApprovalOption{
		{ID: "allow", Kind: "allow"},
		{ID: "reject", Kind: "deny"},
	}
	if _, err := h.registerPendingApproval("ou_user", "approval-1", options); err != nil {
		t.Fatal(err)
	}
	if _, err := h.registerPendingApproval("ou_user", "approval-2", options); err != nil {
		t.Fatal(err)
	}
	reply := platformtest.NewReplier(platform.Capabilities{Text: true})
	runtime := platformMessageRuntime{
		ctx: context.Background(), msg: platform.IncomingMessage{UserID: "ou_user"},
		reply: reply, text: "allow",
	}

	if _, ready := h.preparePlatformMessage(runtime); ready {
		t.Fatal("歧义审批文本不应进入普通消息流程")
	}
	if !containsText(reply.Texts, "多个待审批") || !containsText(reply.Texts, "点击") {
		t.Fatalf("reply=%#v，期望提示点击对应审批卡片", reply.Texts)
	}
}
