package agent

import (
	"reflect"
	"testing"
)

func TestRunAsUserWrapDisabledWhenEmpty(t *testing.T) {
	spec := runAsUserSpec{}
	cmd, args := spec.wrapCommand("claude", []string{"-p", "hi"})
	if cmd != "claude" {
		t.Fatalf("expected command unchanged, got %q", cmd)
	}
	if !reflect.DeepEqual(args, []string{"-p", "hi"}) {
		t.Fatalf("expected args unchanged, got %v", args)
	}
}

func TestRunAsUserWrapBuildsSudoArgv(t *testing.T) {
	spec := runAsUserSpec{User: "coder-bot", PreserveEnv: []string{"ANTHROPIC_API_KEY", "", "BAD=KEY", "PATH", "ANTHROPIC_API_KEY"}}
	if !spec.shouldIsolate() {
		t.Skip("current user equals coder-bot in this environment")
	}
	cmd, args := spec.wrapCommand("claude", []string{"-p", "hi"})
	if cmd != "sudo" {
		t.Fatalf("expected sudo wrapper, got %q", cmd)
	}
	want := []string{"-n", "-u", "coder-bot", "--preserve-env=ANTHROPIC_API_KEY,PATH", "claude", "-p", "hi"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected sudo argv:\n got=%v\nwant=%v", args, want)
	}
}

func TestCleanPreserveEnvDedupAndFilter(t *testing.T) {
	got := cleanPreserveEnv([]string{" A ", "A", "B=1", "", "C"})
	if got != "A,C" {
		t.Fatalf("expected A,C got %q", got)
	}
}

func TestRunAsUserPreservesOnlyExplicitEnvironment(t *testing.T) {
	spec := runAsUserSpec{PreserveEnv: []string{"PATH", " CLAUDE_CODE_OAUTH_TOKEN ", "BAD=KEY"}}
	if !spec.preservesEnv("CLAUDE_CODE_OAUTH_TOKEN") {
		t.Fatal("expected OAuth token to be preserved")
	}
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "BAD=KEY", ""} {
		if spec.preservesEnv(name) {
			t.Fatalf("%q should not be preserved", name)
		}
	}
}
