package messaging

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWorkspaceRegistryAddRemoveRoundTrip(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	stateFile := filepath.Join(t.TempDir(), "workspace-registry.json")
	registry := newWorkspaceRegistry()
	registry.now = func() time.Time { return time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC) }
	if err := registry.SetFilePath(stateFile); err != nil {
		t.Fatalf("SetFilePath: %v", err)
	}

	added, err := registry.Add("codex", alias)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !added.Changed || added.Root != canonicalRoot || added.Revision != 1 {
		t.Fatalf("Add result=%+v, want canonical root and revision 1", added)
	}
	duplicate, err := registry.Add("codex", canonicalRoot)
	if err != nil {
		t.Fatalf("duplicate Add: %v", err)
	}
	if duplicate.Changed || duplicate.Revision != 1 {
		t.Fatalf("duplicate Add result=%+v, want idempotent revision 1", duplicate)
	}

	removed, err := registry.Remove("codex", canonicalRoot)
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !removed.Changed || removed.Revision != 2 {
		t.Fatalf("Remove result=%+v, want changed revision 2", removed)
	}
	repeated, err := registry.Remove("codex", canonicalRoot)
	if err != nil {
		t.Fatalf("repeated Remove: %v", err)
	}
	if repeated.Changed || repeated.Revision != 2 {
		t.Fatalf("repeated Remove result=%+v, want idempotent revision 2", repeated)
	}

	readded, err := registry.Add("codex", canonicalRoot)
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if !readded.Changed || readded.Revision != 3 {
		t.Fatalf("re-add result=%+v, want unhide revision 3", readded)
	}
	info, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode=%v, want 0600", info.Mode().Perm())
	}

	restored := newWorkspaceRegistry()
	if err := restored.SetFilePath(stateFile); err != nil {
		t.Fatalf("restore SetFilePath: %v", err)
	}
	snapshot, err := restored.Snapshot("codex")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 3 || len(snapshot.Registered) != 1 || snapshot.Registered[0].Root != canonicalRoot || snapshot.IsHidden(canonicalRoot) {
		t.Fatalf("restored snapshot=%+v, want one visible registered root", snapshot)
	}
}

func TestWorkspaceRegistryPersistFailureDoesNotPublishAfterImage(t *testing.T) {
	root := t.TempDir()
	registry := newWorkspaceRegistry()
	registry.persist = func(string, workspaceRegistryState) error {
		return errors.New("disk full")
	}
	if err := registry.SetFilePath(filepath.Join(t.TempDir(), "workspace-registry.json")); err != nil {
		t.Fatalf("SetFilePath: %v", err)
	}

	if _, err := registry.Add("claude", root); err == nil {
		t.Fatal("Add error=nil, want persistence failure")
	}
	snapshot, err := registry.Snapshot("claude")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != 0 || len(snapshot.Registered) != 0 || snapshot.IsHidden(root) {
		t.Fatalf("snapshot=%+v, persistence failure published after-image", snapshot)
	}
}

func TestWorkspaceRegistryCorruptStateFailsClosedWithoutOverwrite(t *testing.T) {
	for _, contents := range []string{
		`{`,
		`{"version":99,"revision":7,"agents":{}}`,
	} {
		t.Run(contents, func(t *testing.T) {
			stateFile := filepath.Join(t.TempDir(), "workspace-registry.json")
			if err := os.WriteFile(stateFile, []byte(contents), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			registry := newWorkspaceRegistry()
			if err := registry.SetFilePath(stateFile); err == nil {
				t.Fatal("SetFilePath error=nil, want corrupt-state error")
			}
			if _, err := registry.Add("codex", t.TempDir()); err == nil {
				t.Fatal("Add error=nil after corrupt load")
			}
			got, err := os.ReadFile(stateFile)
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if string(got) != contents {
				t.Fatalf("corrupt state overwritten: got %q want %q", got, contents)
			}
		})
	}
}

func TestMergeWorkspaceRegistryGroupsPreservesNativeOrder(t *testing.T) {
	first := t.TempDir()
	hidden := t.TempDir()
	registeredLater := t.TempDir()
	registeredEarlier := t.TempDir()
	snapshot := workspaceRegistrySnapshot{
		Registered: []workspaceRegistryEntry{
			{Root: registeredLater, AddedAt: "2026-08-06T08:05:00Z"},
			{Root: first, AddedAt: "2026-08-06T08:00:00Z"},
			{Root: registeredEarlier, AddedAt: "2026-08-06T08:01:00Z"},
		},
		Hidden: map[string]struct{}{hidden: {}},
	}
	native := []codexWorkspaceGroup{
		{Name: "first-native", Root: first},
		{Name: "hidden-native", Root: hidden},
	}

	got := mergeWorkspaceRegistryGroups(native, snapshot)
	wantRoots := []string{first}
	for _, root := range []string{registeredEarlier, registeredLater} {
		canonical, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatalf("resolve expected root: %v", err)
		}
		wantRoots = append(wantRoots, canonical)
	}
	gotRoots := make([]string, 0, len(got))
	for _, group := range got {
		gotRoots = append(gotRoots, group.Root)
	}
	if !reflect.DeepEqual(gotRoots, wantRoots) {
		t.Fatalf("merged roots=%v, want %v", gotRoots, wantRoots)
	}
	if got[0].Name != "first-native" {
		t.Fatalf("native label=%q, want preserved", got[0].Name)
	}
}
