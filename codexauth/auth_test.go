package codexauth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func testAuthJSON(t *testing.T, email, accountID string) []byte {
	t.Helper()
	claims, err := json.Marshal(map[string]string{"email": email})
	if err != nil {
		t.Fatal(err)
	}
	idToken := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	data, err := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"last_refresh":   "2026-07-19T00:00:00Z",
		"tokens": map[string]string{
			"access_token":  "access-secret",
			"refresh_token": "refresh-secret",
			"id_token":      idToken,
			"account_id":    accountID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseSnapshotAcceptsChatGPTOAuthAndRedactsIdentity(t *testing.T) {
	snapshot, err := ParseSnapshot(testAuthJSON(t, "alice@example.com", "acct-1"))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AuthMode() != "chatgpt" || snapshot.EmailMasked() != "a***e@example.com" {
		t.Fatalf("snapshot mode=%q email=%q", snapshot.AuthMode(), snapshot.EmailMasked())
	}
	if !snapshot.MatchesEmail("Alice@Example.com") {
		t.Fatal("snapshot should match normalized account email")
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "access-secret") || strings.Contains(string(encoded), "refresh-secret") {
		t.Fatalf("snapshot JSON leaked credentials: %s", encoded)
	}
}

func TestParseSnapshotRejectsUnsupportedAndIncompleteAuth(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		code string
	}{
		{name: "api key", data: []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"secret"}`), code: CodeUnsupportedAuth},
		{name: "pat", data: []byte(`{"auth_mode":"pat","token":"secret"}`), code: CodeUnsupportedAuth},
		{name: "bedrock", data: []byte(`{"auth_mode":"bedrock","credentials":{"secret":"value"}}`), code: CodeUnsupportedAuth},
		{name: "oauth with api key", data: func() []byte {
			var auth map[string]any
			if err := json.Unmarshal(testAuthJSON(t, "alice@example.com", "acct-1"), &auth); err != nil {
				t.Fatal(err)
			}
			auth["OPENAI_API_KEY"] = "secret"
			data, err := json.Marshal(auth)
			if err != nil {
				t.Fatal(err)
			}
			return data
		}(), code: CodeUnsupportedAuth},
		{name: "missing refresh", data: []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"a","id_token":"x","account_id":"id"}}`), code: CodeUnsupportedAuth},
		{name: "invalid json", data: []byte(`{`), code: CodeInvalid},
		{name: "trailing json", data: append(testAuthJSON(t, "alice@example.com", "acct-1"), []byte(` {}`)...), code: CodeInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSnapshot(test.data)
			if ErrorCode(err) != test.code {
				t.Fatalf("ParseSnapshot() error=%v code=%q, want %q", err, ErrorCode(err), test.code)
			}
		})
	}
}

func TestParseSnapshotRejectsJWTWithoutEmail(t *testing.T) {
	data := testAuthJSON(t, "alice@example.com", "acct-1")
	var auth map[string]any
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatal(err)
	}
	tokens := auth["tokens"].(map[string]any)
	tokens["id_token"] = "e30.e30.signature"
	data, _ = json.Marshal(auth)
	_, err := ParseSnapshot(data)
	if ErrorCode(err) != CodeUnsupportedAuth {
		t.Fatalf("ParseSnapshot() error=%v", err)
	}
}
