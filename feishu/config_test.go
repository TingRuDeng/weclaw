package feishu

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

func TestSaveAndLoadCredentialsUsesSecureFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	creds := Credentials{AppID: "cli_a", AppSecret: "secret-a"}

	if err := SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}
	path, err := CredentialsPath()
	if err != nil {
		t.Fatalf("CredentialsPath error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials mode=%o, want 600", mode)
	}
	loaded, err := LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials error: %v", err)
	}
	if loaded != creds {
		t.Fatalf("credentials=%#v, want %#v", loaded, creds)
	}
}

func TestSaveAndLoadCredentialsForBotUsesIsolatedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	first := Credentials{AppID: "cli_a", AppSecret: "secret-a"}
	second := Credentials{AppID: "cli_b", AppSecret: "secret-b"}

	if err := SaveCredentialsForBot("project-a", first); err != nil {
		t.Fatalf("SaveCredentialsForBot project-a error: %v", err)
	}
	if err := SaveCredentialsForBot("project-b", second); err != nil {
		t.Fatalf("SaveCredentialsForBot project-b error: %v", err)
	}
	path, err := CredentialsPathForBot("project-a")
	if err != nil {
		t.Fatalf("CredentialsPathForBot error: %v", err)
	}
	wantPath := filepath.Join(home, ".weclaw", "platforms", "feishu", "project-a.json")
	if path != wantPath {
		t.Fatalf("path=%q, want %q", path, wantPath)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials mode=%o, want 600", mode)
	}
	loaded, err := LoadCredentialsForBot("project-a")
	if err != nil {
		t.Fatalf("LoadCredentialsForBot error: %v", err)
	}
	if loaded != first {
		t.Fatalf("credentials=%#v, want %#v", loaded, first)
	}
}

func TestCredentialsPathForBotRejectsUnsafeName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := CredentialsPathForBot("../project")

	if err == nil || !strings.Contains(err.Error(), "invalid feishu bot name") {
		t.Fatalf("CredentialsPathForBot error=%v, want invalid name", err)
	}
}

func TestLoadCredentialsPrefersEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(envAppID, "cli_env")
	t.Setenv(envAppSecret, "secret-env")
	if err := SaveCredentials(Credentials{AppID: "cli_file", AppSecret: "secret-file"}); err != nil {
		t.Fatalf("SaveCredentials error: %v", err)
	}

	record, err := LoadCredentialsWithSource()
	if err != nil {
		t.Fatalf("LoadCredentialsWithSource error: %v", err)
	}
	if record.Source != "env" || record.Credentials.AppID != "cli_env" {
		t.Fatalf("record=%#v, want env credentials", record)
	}
}

func TestLoadCredentialsMissingFileReportsLoginHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := LoadCredentials()

	if err == nil || !strings.Contains(err.Error(), "weclaw feishu login") {
		t.Fatalf("LoadCredentials error=%v, want login hint", err)
	}
}

func TestValidateCredentialsPostsAppCredentials(t *testing.T) {
	oldURL := tenantTokenURL
	defer func() { tenantTokenURL = oldURL }()
	var got map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"token"}`))
	}))
	defer server.Close()
	tenantTokenURL = server.URL

	creds := Credentials{AppID: "cli_a", AppSecret: "secret-a"}
	if err := ValidateCredentials(context.Background(), creds); err != nil {
		t.Fatalf("ValidateCredentials error: %v", err)
	}
	if got["app_id"] != creds.AppID || got["app_secret"] != creds.AppSecret {
		t.Fatalf("request=%#v, want credentials", got)
	}
}

func TestValidateCredentialsErrorDoesNotExposeSecret(t *testing.T) {
	oldURL := tenantTokenURL
	defer func() { tenantTokenURL = oldURL }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":99991400,"msg":"permission denied"}`))
	}))
	defer server.Close()
	tenantTokenURL = server.URL

	secret := "secret-should-not-appear"
	err := ValidateCredentials(context.Background(), Credentials{AppID: "cli_a", AppSecret: secret})

	if err == nil {
		t.Fatal("ValidateCredentials error=nil, want failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks secret: %v", err)
	}
}

func TestValidateCredentialsPermissionErrorUsesUnifiedClassifier(t *testing.T) {
	oldURL := tenantTokenURL
	defer func() { tenantTokenURL = oldURL }()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":99991663,"msg":"missing scope"}`))
	}))
	defer server.Close()
	tenantTokenURL = server.URL

	err := ValidateCredentials(context.Background(), Credentials{AppID: "cli_a", AppSecret: "secret"})

	if !IsPermissionError(err) {
		t.Fatalf("ValidateCredentials error=%v, want unified permission error", err)
	}
}

func TestCredentialsPathUsesWeclawPlatformsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := CredentialsPath()
	if err != nil {
		t.Fatalf("CredentialsPath error: %v", err)
	}
	want := filepath.Join(home, ".weclaw", "platforms", "feishu.json")
	if path != want {
		t.Fatalf("path=%q, want %q", path, want)
	}
}
