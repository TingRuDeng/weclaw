package antigravity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConversationServiceClientSendsOfficialHeaders(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("Connect-Protocol-Version") != "1" {
			t.Errorf("connect version = %q", request.Header.Get("Connect-Protocol-Version"))
		}
		if request.Header.Get("x-antigravity-csrf-token") != "secret-token" {
			t.Errorf("csrf header = %q", request.Header.Get("x-antigravity-csrf-token"))
		}
		seen = append(seen, request.URL.Path)
		switch request.URL.Path {
		case "/product.v1.ConversationService/ListConversations":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["pageToken"] == "page-2" {
				_ = json.NewEncoder(writer).Encode(map[string]any{"conversations": []map[string]any{{
					"id":          "c-summary",
					"metadata":    map[string]any{"projectId": "p2"},
					"annotations": map[string]any{},
					"state":       map[string]any{"summary": "Fallback summary title", "runState": "RUN_STATE_IDLE"},
				}}})
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"conversations": []map[string]any{{
					"id":          "c1",
					"metadata":    map[string]any{"projectId": "p1", "parentConversationId": "parent"},
					"annotations": map[string]any{"title": "One", "archived": true},
					"state":       map[string]any{"summary": "ignored summary", "runState": "RUN_STATE_RUNNING"},
				}},
				"nextPageToken": "page-2",
			})
		case "/product.v1.ConversationService/GetConversation":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["conversationId"] != "c1" {
				t.Errorf("get body = %+v", body)
			}
			if _, hasSnake := body["conversation_id"]; hasSnake {
				t.Errorf("get body must not send duplicate conversation_id: %+v", body)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id":          "c1",
				"metadata":    map[string]any{"projectId": "p1"},
				"annotations": map[string]any{"title": "One"},
				"state":       map[string]any{"runState": "RUN_STATE_RUNNING"},
			})
		case "/product.v1.ConversationService/CreateConversation":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["projectId"] != "p1" || body["title"] != "New" || body["model"] != "m" {
				t.Errorf("create body = %+v", body)
			}
			prompt, _ := body["prompt"].([]any)
			if len(prompt) != 1 {
				t.Errorf("prompt = %+v", prompt)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"conversation": map[string]any{
				"id":          "c2",
				"metadata":    map[string]any{"projectId": "p1"},
				"annotations": map[string]any{"title": "New"},
				"state":       map[string]any{"runState": "RUN_STATE_RUNNING"},
			}})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	client, err := NewConversationServiceClient(strings.TrimPrefix(server.URL, "http://"), "secret-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	conversations, err := client.ListConversations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(conversations) != 2 || !conversations[0].Archived || conversations[0].ParentConversationID != "parent" ||
		conversations[0].ProjectID != "p1" || conversations[0].Title != "One" || conversations[0].RunState != "RUN_STATE_RUNNING" {
		t.Fatalf("list[0] = %+v", conversations)
	}
	if conversations[1].ID != "c-summary" || conversations[1].ProjectID != "p2" || conversations[1].Title != "Fallback summary title" {
		t.Fatalf("list[1] = %+v", conversations[1])
	}
	conversation, err := client.GetConversation(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.RunState != "RUN_STATE_RUNNING" || conversation.ProjectID != "p1" || conversation.Title != "One" {
		t.Fatalf("get = %+v", conversation)
	}
	created, err := client.CreateConversation(context.Background(), CreateConversationRequest{
		ProjectID: "p1", Title: "New", Model: "m", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "c2" || created.ProjectID != "p1" {
		t.Fatalf("create = %+v", created)
	}
	if len(seen) != 4 {
		t.Fatalf("paths = %v", seen)
	}
}

func TestConversationServiceClientRejectsScheme(t *testing.T) {
	if _, err := NewConversationServiceClient("http://127.0.0.1:1", "token", nil); err == nil {
		t.Fatal("address with scheme must be rejected")
	}
	if _, err := NewConversationServiceClient("127.0.0.1:1", "", nil); err == nil {
		t.Fatal("empty CSRF token must be rejected")
	}
}

func TestConversationServiceClientDoesNotLeakCSRFToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "failure token super-secret-csrf", http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewConversationServiceClient(strings.TrimPrefix(server.URL, "http://"), "super-secret-csrf", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListConversations(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "super-secret-csrf") {
		t.Fatalf("CSRF token leaked in error: %v", err)
	}
}

func TestLoadProjectsMapsNamesAndFallbacks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p1.json"), []byte(`{"name":"Alpha"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p2.json"), []byte(`{"projectResources":{"resources":[{"folderUri":"file:///work/beta"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "p3.json"), []byte(`{"name":"outside-of-project"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	projects := LoadProjects(dir)
	if projects["p1"].Name != "Alpha" || projects["p2"].Name != "beta" {
		t.Fatalf("projects = %+v", projects)
	}
	if ProjectName(projects, "p3") != "未归属项目" || ProjectName(projects, "") != "未归属项目" {
		t.Fatalf("outside project name = %q", ProjectName(projects, "p3"))
	}
	if ProjectName(projects, "unknown") != "unknown" {
		t.Fatalf("unknown project name = %q", ProjectName(projects, "unknown"))
	}
}
