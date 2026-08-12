package feishu

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/fastclaw-ai/weclaw/platform"
)

type cardFollowerCodexAgent struct{}

func (cardFollowerCodexAgent) Chat(context.Context, string, string) (string, error) { return "", nil }
func (cardFollowerCodexAgent) ResetSession(context.Context, string) (string, error) { return "", nil }
func (cardFollowerCodexAgent) Info() agent.AgentInfo {
	return agent.AgentInfo{Name: "codex", Type: "acp", Command: "codex"}
}
func (cardFollowerCodexAgent) SetCwd(string) {}
func (cardFollowerCodexAgent) InspectCodexRuntime(
	_ context.Context, req agent.CodexRuntimeRequest,
) (agent.CodexThreadBinding, error) {
	return cardFollowerRuntimeBinding(req), nil
}
func (cardFollowerCodexAgent) CurrentCodexRuntime(req agent.CodexRuntimeRequest) (agent.CodexThreadBinding, error) {
	return cardFollowerRuntimeBinding(req), nil
}
func (cardFollowerCodexAgent) HandoffCodexRuntime(
	_ context.Context, req agent.CodexRuntimeRequest,
) (agent.CodexThreadBinding, error) {
	return cardFollowerRuntimeBinding(req), nil
}
func (cardFollowerCodexAgent) ReconcileCodexObservedTurn(
	_ context.Context, req agent.CodexRuntimeRequest, state agent.CodexThreadState,
) (agent.CodexThreadBinding, error) {
	binding := cardFollowerRuntimeBinding(req)
	binding.State = state
	return binding, nil
}
func (cardFollowerCodexAgent) MarkCodexRuntimeConflict(context.Context, agent.CodexRuntimeRequest) error {
	return nil
}
func (cardFollowerCodexAgent) RunCodexTurn(context.Context, agent.CodexTurnRequest) (string, error) {
	return "", nil
}

func cardFollowerRuntimeBinding(req agent.CodexRuntimeRequest) agent.CodexThreadBinding {
	return agent.CodexThreadBinding{
		Ref: req.Ref, Control: req.Intent, Runtime: agent.CodexRuntimeWeClaw,
		State: agent.CodexThreadState{ThreadID: req.Ref.ThreadID},
	}
}

type persistedCardFollowerState struct {
	Bindings map[string]struct {
		ActiveWorkspace string
		Workspaces      map[string]struct {
			ThreadID string
		}
		FollowRevision uint64
		Follower       *struct {
			WorkspaceRoot      string
			ThreadID           string
			ActorUserID        string
			AuthorizedIdentity string
			DeliveryRoute      platform.DeliveryRoute
			UpdatedAt          string
		}
	}
}

func TestCodexSwitchCardCallbackPersistsFollowerDeliveryRoute(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "codex-sessions.json")
	h := messaging.NewHandler(nil, nil)
	h.SetCodexSessionFile(statePath)
	if err := h.SetAgentSessionFile(filepath.Join(root, "agent-sessions.json")); err != nil {
		t.Fatal(err)
	}
	if err := h.SetWorkspaceRegistryFile(filepath.Join(root, "workspace-registry.json")); err != nil {
		t.Fatal(err)
	}
	h.SetCodexLocalSessionDir(filepath.Join(root, "codex-home"))
	h.SetAllowedWorkspaceRoots([]string{workspace})
	h.SetAgentWorkDirs(map[string]string{"codex": workspace})
	h.SetDefaultAgent("codex", cardFollowerCodexAgent{})

	adapter := NewAdapter(Credentials{AppID: "cli_a", AppSecret: "secret"})
	adapter.sender = &fakeMessageSender{}
	registry := platform.NewRegistry([]platform.RegistryEntry{{
		Platform: adapter, Access: platform.NewAccessControl([]string{"ou_user"}),
	}})
	h.SetPlatformRegistry(registry)
	sessionKey := BuildFeishuSessionKey(FeishuSessionScope{
		AccountID: "cli_a", TenantID: "tenant_1", ChatID: "oc_1",
		SenderOpenID: "ou_user", ChatType: "p2p",
	})
	type wrapperObservation struct {
		inline   bool
		deferred bool
		base     bool
		route    platform.DeliveryRoute
	}
	observed := make(chan wrapperObservation, 1)
	response, err := adapter.handleCardActionEvent(
		context.Background(), cardChoiceEventForOrderTest(sessionKey),
		func(ctx context.Context, msg platform.IncomingMessage, reply platform.Replier) {
			observation := wrapperObservation{}
			if inline, ok := reply.(*inlineCardReplier); ok {
				observation.inline = true
				if deferred, ok := inline.Replier.(*deferredCardResultReplier); ok {
					observation.deferred = true
					if base, ok := deferred.Replier.(*Replier); ok {
						observation.base = true
						observation.route = base.DeliveryRoute()
					}
				}
			}
			observed <- observation
			authorized, ok := registry.AuthorizeIncomingMessage(msg)
			if !ok {
				t.Error("card callback message was not authorized by the production registry")
				return
			}
			h.HandleMessage(ctx, authorized, reply)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Toast == nil || response.Toast.Type != "success" {
		t.Fatalf("callback response=%#v", response)
	}
	cardJSON, err := json.Marshal(response.Card.Data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cardJSON), "已切换并绑定") {
		t.Fatalf("callback card=%s", cardJSON)
	}
	observation := <-observed
	wantRoute := platform.DeliveryRoute{
		Platform: platform.PlatformFeishu, AccountID: "cli_a", ChatID: "oc_1",
	}
	if !observation.inline || !observation.deferred || !observation.base || observation.route != wantRoute {
		t.Fatalf("wrapper observation=%#v, want route=%#v", observation, wantRoute)
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state persistedCardFollowerState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	binding, ok := state.Bindings[sessionKey+"\x00codex"]
	if !ok {
		t.Fatalf("bindings=%#v, missing card session binding", state.Bindings)
	}
	if binding.Follower == nil {
		t.Fatal("Follower=nil; DeliveryRoute lost through *inlineCardReplier -> *deferredCardResultReplier")
	}
	if binding.FollowRevision != 1 || binding.ActiveWorkspace != workspace ||
		binding.Workspaces[workspace].ThreadID != "thread-a" ||
		binding.Follower.WorkspaceRoot != workspace || binding.Follower.ThreadID != "thread-a" ||
		binding.Follower.ActorUserID != "ou_user" || binding.Follower.AuthorizedIdentity != "ou_user" ||
		binding.Follower.DeliveryRoute != wantRoute {
		t.Fatalf("persisted binding=%#v", binding)
	}
	if _, err := time.Parse(time.RFC3339, binding.Follower.UpdatedAt); err != nil {
		t.Fatalf("follower updatedAt=%q: %v", binding.Follower.UpdatedAt, err)
	}
}
