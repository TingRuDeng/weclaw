package agent

import (
	"reflect"
	"testing"
)

func TestCodexDesktopPatchAppliesAddReplaceRemove(t *testing.T) {
	baseline := map[string]any{
		"meta":  map[string]any{"model": "old", "remove": true},
		"turns": []any{map[string]any{"id": "turn-1"}},
	}
	patches := []codexDesktopPatch{
		{Op: "replace", Path: []any{"meta", "model"}, Value: "new"},
		{Op: "remove", Path: []any{"meta", "remove"}},
		{Op: "add", Path: []any{"turns", 1}, Value: map[string]any{"id": "turn-2"}},
	}

	got, err := applyCodexDesktopPatches(baseline, patches)
	if err != nil {
		t.Fatalf("applyCodexDesktopPatches() error = %v", err)
	}
	want := map[string]any{
		"meta":  map[string]any{"model": "new"},
		"turns": []any{map[string]any{"id": "turn-1"}, map[string]any{"id": "turn-2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("patched state = %#v, want %#v", got, want)
	}
	if baseline["meta"].(map[string]any)["model"] != "old" {
		t.Fatal("patch 修改了输入 baseline")
	}
}

func TestCodexDesktopPatchAllowsRootReplace(t *testing.T) {
	got, err := applyCodexDesktopPatches(
		map[string]any{"id": "old"},
		[]codexDesktopPatch{{Op: "replace", Path: nil, Value: map[string]any{"id": "new"}}},
	)
	if err != nil || got["id"] != "new" {
		t.Fatalf("root replace = %#v, %v", got, err)
	}
}

func TestCodexDesktopPatchRejectsInvalidPathAndIndex(t *testing.T) {
	tests := []struct {
		name  string
		patch codexDesktopPatch
	}{
		{name: "missing object key", patch: codexDesktopPatch{Op: "replace", Path: []any{"missing"}, Value: true}},
		{name: "array index overflow", patch: codexDesktopPatch{Op: "add", Path: []any{"turns", 2}, Value: true}},
		{name: "fractional array index", patch: codexDesktopPatch{Op: "remove", Path: []any{"turns", 0.5}}},
		{name: "root remove", patch: codexDesktopPatch{Op: "remove", Path: nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseline := map[string]any{"turns": []any{"first"}}
			if _, err := applyCodexDesktopPatches(baseline, []codexDesktopPatch{test.patch}); err == nil {
				t.Fatal("applyCodexDesktopPatches() error = nil")
			}
			if !reflect.DeepEqual(baseline, map[string]any{"turns": []any{"first"}}) {
				t.Fatal("失败 patch 修改了输入 baseline")
			}
		})
	}
}

func TestCodexDesktopPatchCommitsAtomically(t *testing.T) {
	baseline := map[string]any{"model": "old"}
	patches := []codexDesktopPatch{
		{Op: "replace", Path: []any{"model"}, Value: "new"},
		{Op: "remove", Path: []any{"missing"}},
	}

	if _, err := applyCodexDesktopPatches(baseline, patches); err == nil {
		t.Fatal("applyCodexDesktopPatches() error = nil")
	}
	if baseline["model"] != "old" {
		t.Fatalf("baseline model = %v", baseline["model"])
	}
}
