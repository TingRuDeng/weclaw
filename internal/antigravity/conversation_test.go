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
			_ = json.NewEncoder(writer).Encode(map[string]any{"conversations": []map[string]any{{
				"id": "c1", "title": "One", "projectId": "p1", "parentConversationId": "parent", "archived": true,
			}}})
		case "/product.v1.ConversationService/GetConversation":
			_ = json.NewEncoder(writer).Encode(map[string]any{"conversation": map[string]any{
				"id": "c1", "title": "One", "runState": "RUN_STATE_RUNNING",
			}})
		case "/product.v1.ConversationService/CreateConversation":
			var body map[string]any
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["project_id"] != "p1" || body["title"] != "New" || body["model"] != "m" {
				t.Errorf("create body = %+v", body)
			}
			messages, _ := body["messages"].([]any)
			if len(messages) != 1 {
				t.Errorf("messages = %+v", messages)
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"conversation": map[string]any{"id": "c2", "title": "New"}})
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
	if len(conversations) != 1 || !conversations[0].Archived || conversations[0].ParentConversationID != "parent" {
		t.Fatalf("list = %+v", conversations)
	}
	conversation, err := client.GetConversation(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if conversation.RunState != "RUN_STATE_RUNNING" {
		t.Fatalf("get = %+v", conversation)
	}
	created, err := client.CreateConversation(context.Background(), CreateConversationRequest{
		ProjectID: "p1", Title: "New", Model: "m", Prompt: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "c2" {
		t.Fatalf("create = %+v", created)
	}
	if len(seen) != 3 {
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
