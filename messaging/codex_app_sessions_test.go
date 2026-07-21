package messaging

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeCodexAppWorkspacesKeepsNewProjectsAheadOfSavedOrder(t *testing.T) {
	root := t.TempDir()
	weclawRoot := filepath.Join(root, "weclaw")
	doraemonRoot := filepath.Join(root, "doraemon")
	smartHomeRoot := filepath.Join(root, ".codex", ".chatgpt-projects", "g-p-smart-home")
	mustCreateWorkspaceDirs(t, weclawRoot, doraemonRoot, smartHomeRoot)

	workspaces := mergeCodexAppWorkspaces(codexAppWorkspaceState{
		ProjectOrder: []string{"local-weclaw", "local-doraemon"},
		LocalProjects: map[string]codexAppProject{
			"local-weclaw": {
				Name:      "weclaw",
				RootPaths: []string{weclawRoot},
				UpdatedAt: 100,
			},
			"local-doraemon": {
				Name:      "doraemon",
				RootPaths: []string{doraemonRoot},
				UpdatedAt: 200,
			},
			"g-p-smart-home": {
				Name:      "智能家居总控",
				RootPaths: []string{smartHomeRoot},
				UpdatedAt: 300,
			},
		},
	}, nil)

	wantNames := []string{"智能家居总控", "weclaw", "doraemon"}
	if len(workspaces) != len(wantNames) {
		t.Fatalf("workspace count = %d, want %d: %#v", len(workspaces), len(wantNames), workspaces)
	}
	for i, want := range wantNames {
		if workspaces[i].Name != want {
			t.Fatalf("workspace[%d].Name = %q, want %q; all=%#v", i, workspaces[i].Name, want, workspaces)
		}
	}
}

func TestMergeCodexAppWorkspacesUsesAssignedThreadRecencyForNewProjects(t *testing.T) {
	codexDir := t.TempDir()
	root := t.TempDir()
	activeRoot := filepath.Join(root, "active")
	staleRoot := filepath.Join(root, "stale")
	orderedRoot := filepath.Join(root, "ordered")
	mustCreateWorkspaceDirs(t, activeRoot, staleRoot, orderedRoot)
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatalf("write fake state database: %v", err)
	}
	writeFakeSQLite3(t, fmt.Sprintf(
		`[{"id":"thread-active","cwd":%q,"recency_at_ms":400,"thread_source":"user"}]`,
		filepath.Join(root, "different-cwd"),
	))

	state := codexAppWorkspaceState{
		ProjectOrder: []string{"local-ordered"},
		LocalProjects: map[string]codexAppProject{
			"local-active": {
				Name:      "active",
				RootPaths: []string{activeRoot},
				UpdatedAt: 100,
			},
			"local-stale": {
				Name:      "stale",
				RootPaths: []string{staleRoot},
				UpdatedAt: 300,
			},
			"local-ordered": {
				Name:      "ordered",
				RootPaths: []string{orderedRoot},
				UpdatedAt: 500,
			},
		},
		ThreadProjectAssignments: map[string]codexAppThreadProjectAssignment{
			"thread-active": {ProjectKind: "local", ProjectID: "local-active"},
		},
	}

	projectRecency, err := readCodexAppProjectRecency(codexDir, state)
	if err != nil {
		t.Fatalf("read project recency: %v", err)
	}
	workspaces := mergeCodexAppWorkspaces(state, projectRecency)
	wantNames := []string{"active", "stale", "ordered"}
	if len(workspaces) != len(wantNames) {
		t.Fatalf("workspace count = %d, want %d: %#v", len(workspaces), len(wantNames), workspaces)
	}
	for i, want := range wantNames {
		if workspaces[i].Name != want {
			t.Fatalf("workspace[%d].Name = %q, want %q; all=%#v", i, workspaces[i].Name, want, workspaces)
		}
	}
}

func TestReadCodexAppWorkspacesDistinguishesMissingAndCorruptState(t *testing.T) {
	missingDir := t.TempDir()
	workspaces, available, err := readCodexAppWorkspaces(missingDir)
	if err != nil || available || workspaces != nil {
		t.Fatalf("missing state=(%#v,%t,%v), want unavailable without error", workspaces, available, err)
	}

	corruptDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(corruptDir, ".codex-global-state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspaces, available, err = readCodexAppWorkspaces(corruptDir)
	if err == nil || !available || workspaces != nil {
		t.Fatalf("corrupt state=(%#v,%t,%v), want available catalog with explicit error", workspaces, available, err)
	}
}

func TestCodexAppCatalogTreatsEmptySQLiteOutputAsValidEmptyResult(t *testing.T) {
	codexDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexDir, "state_5.sqlite"), []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeSQLite3(t, "")

	recency, err := readCodexAppProjectRecency(codexDir, codexAppWorkspaceState{})
	if err != nil || len(recency) != 0 {
		t.Fatalf("recency=(%#v,%v), want valid empty result", recency, err)
	}
	sessions, available, err := readCodexAppWorkspaceThreads(codexDir, workspace)
	if err != nil || !available || len(sessions) != 0 {
		t.Fatalf("sessions=(%#v,%t,%v), want authoritative empty result", sessions, available, err)
	}
}

func TestCodexWorkspaceListDoesNotFallbackWhenAppStateIsCorrupt(t *testing.T) {
	codexDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(codexDir, ".codex-global-state.json"), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(nil, nil)
	h.SetCodexLocalSessionDir(codexDir)

	groups, err := h.codexWorkspaceListForAccess("binding", true)
	if err == nil || groups != nil {
		t.Fatalf("groups=%#v err=%v, corrupt authoritative state must not use historical fallback", groups, err)
	}
	if got := h.renderCodexWorkspaceListForAccess("binding", "admin", true); got == "" || got == "当前还没有 Codex 工作空间。" {
		t.Fatalf("render=%q, want explicit degraded-state message", got)
	}
}
