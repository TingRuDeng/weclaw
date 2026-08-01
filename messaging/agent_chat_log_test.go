package messaging

import (
	"bytes"
	"context"
	"log"
	"strings"
	"testing"
)

func TestAgentReplyLogDoesNotContainReplyBody(t *testing.T) {
	handler := newTestHandler()
	agentReply := "top-secret-agent-reply-模型正文"
	ag := &fakeAgent{reply: agentReply}
	var logs bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousOutput) })

	reply, err := handler.chatWithAgentWithProgressEvents(context.Background(), ag, "user-1", "hello", nil)
	if err != nil {
		t.Fatalf("chatWithAgentWithProgressEvents error: %v", err)
	}
	if reply != agentReply {
		t.Fatalf("reply=%q, want original agent reply", reply)
	}
	if strings.Contains(logs.String(), agentReply) || strings.Contains(logs.String(), "top-secret-agent-reply") {
		t.Fatalf("agent log contains reply body: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "runes=") {
		t.Fatalf("agent log=%q, want reply length metadata", logs.String())
	}
}
