package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCodexAppBridgeNativeToolClientUsesExistingHost(t *testing.T) {
	for _, args := range [][]string{
		{"app-server", "--listen", "stdio://"},
		{"app-server", "--listen=stdio://"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			a, socket, dir := newCodexAppToolClientTestAgent(t)
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan string, 4)
			serverDone := make(chan struct{})
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer close(serverDone)
				defer conn.Close()
				for {
					var request struct {
						ID     int    `json:"id"`
						Method string `json:"method"`
					}
					if err := conn.ReadJSON(&request); err != nil {
						return
					}
					requests <- request.Method
					result := map[string]any{"userAgent": "test-host"}
					if request.Method == "getAuthStatus" {
						result = map[string]any{"authMethod": "apikey", "requiresOpenaiAuth": true}
					}
					if err := conn.WriteJSON(map[string]any{"id": request.ID, "result": result}); err != nil {
						return
					}
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			preflight := false
			a.codexHostConflictPreflightCall = func(context.Context, int) error {
				preflight = true
				return nil
			}
			input, inputWriter := io.Pipe()
			defer input.Close()
			defer inputWriter.Close()
			output, outputWriter := io.Pipe()
			defer output.Close()
			defer outputWriter.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				err := a.RunCodexAppBridge(ctx, args, input, outputWriter, io.Discard)
				_ = outputWriter.CloseWithError(err)
				_ = input.CloseWithError(err)
				done <- err
			}()
			reader := json.NewDecoder(output)
			for id, method := range []string{"initialize", "getAuthStatus"} {
				payload, _ := json.Marshal(map[string]any{"id": id + 1, "method": method, "params": map[string]any{}})
				if _, err := inputWriter.Write(append(payload, '\n')); err != nil {
					t.Fatalf("native tool client could not initialize against shared Host: %v", err)
				}
				var response struct {
					ID     int            `json:"id"`
					Result map[string]any `json:"result"`
				}
				if err := reader.Decode(&response); err != nil || response.ID != id+1 {
					t.Fatalf("unexpected shared Host response %+v: %v", response, err)
				}
				if method == "getAuthStatus" && response.Result["authMethod"] != "apikey" {
					t.Fatalf("shared Host auth state was not relayed: %+v", response)
				}
				if got := <-requests; got != method {
					t.Fatalf("unexpected RPC %q, want %q", got, method)
				}
			}
			_ = inputWriter.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("native tool client did not disconnect")
			}
			if !preflight {
				t.Fatal("native tool client skipped shared Host identity validation")
			}
			select {
			case <-serverDone:
			case <-ctx.Done():
				t.Fatal("native tool client left its Host connection open")
			}
			assertCodexAppToolClientPreservedState(t, dir)
		})
	}
}

func TestCodexAppBridgeNativeToolClientRejectsUnknownHost(t *testing.T) {
	a, socket, dir := newCodexAppToolClientTestAgent(t)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := newFakeCodexHost(listener)
	server.start(t)
	want := errors.New("unverified Host identity")
	a.codexHostConflictPreflightCall = func(context.Context, int) error { return want }
	err = a.RunCodexAppBridge(context.Background(), []string{"app-server", "--listen", "stdio://"}, strings.NewReader(""), io.Discard, io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want Host identity rejection", err)
	}
	assertCodexAppToolClientPreservedState(t, dir)
}

func TestCodexAppBridgeNativeToolClientDoesNotStartMissingHost(t *testing.T) {
	a, _, dir := newCodexAppToolClientTestAgent(t)
	err := a.RunCodexAppBridge(context.Background(), []string{"app-server", "--listen", "stdio://"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "共享 Host") {
		t.Fatalf("error=%v, want unavailable shared Host", err)
	}
	assertCodexAppToolClientPreservedState(t, dir)
}

func TestCodexAppBridgeNativeToolClientRejectsUnsafeSocket(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "shared directory", want: "must not be accessible"},
		{name: "symlink socket", want: "non-socket"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, socket, dir := newCodexAppToolClientTestAgent(t)
			target := socket
			if test.name == "symlink socket" {
				target += ".real"
			}
			listener, err := net.Listen("unix", target)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			newFakeCodexHost(listener).start(t)
			if test.name == "symlink socket" {
				err = os.Symlink(target, socket)
			} else {
				err = os.Chmod(filepath.Dir(socket), 0o755)
			}
			if err != nil {
				t.Fatal(err)
			}
			a.codexHostConflictPreflightCall = func(context.Context, int) error { return nil }
			err = a.RunCodexAppBridge(context.Background(), []string{"app-server", "--listen", "stdio://"}, strings.NewReader(""), io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want unsafe socket rejected before use", err)
			}
			assertCodexAppToolClientPreservedState(t, dir)
		})
	}
}

func newCodexAppToolClientTestAgent(t *testing.T) (*ACPAgent, string, string) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "weclaw-app-tools-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	socket := codexAppSharedSocketForHome(home)
	dir := codexAppBridgeDirectory(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app-args.json", "app-pipe.json", "app-pipe-applied.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("original "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binary := codexDaemonManagedBinaryPath(home)
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	a := NewACPAgent(ACPAgentConfig{Command: binary, Args: []string{"app-server"}, CodexHostMode: "shared", Env: map[string]string{"CODEX_HOME": home}})
	return a, socket, dir
}

func assertCodexAppToolClientPreservedState(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("native tool client wrote shared Host launch artifacts: %v", entries)
	}
	for _, name := range []string{"app-args.json", "app-pipe.json", "app-pipe-applied.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "original "+name {
			t.Fatalf("native tool client changed %s: %q %v", name, data, err)
		}
	}
}
