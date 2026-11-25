package cmd

import (
	"strings"
	"testing"
)

func TestAgSidecarCommandRegistered(t *testing.T) {
	found := false
	for _, child := range rootCmd.Commands() {
		if child.Name() == "ag-sidecar" {
			found = true
			if child.Flags().Lookup("socket") == nil {
				t.Fatal("ag-sidecar must expose --socket")
			}
		}
	}
	if !found {
		t.Fatal("ag-sidecar is not registered on the root command")
	}
}

func TestAgSidecarRequiresUpstreamConfiguration(t *testing.T) {
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "")
	t.Setenv("ANTIGRAVITY_CSRF_TOKEN", "")
	err := runAgSidecar(agSidecarCmd)
	if err == nil || !strings.Contains(err.Error(), "ANTIGRAVITY_LS_ADDRESS") {
		t.Fatalf("error = %v", err)
	}
	t.Setenv("ANTIGRAVITY_LS_ADDRESS", "127.0.0.1:1")
	err = runAgSidecar(agSidecarCmd)
	if err == nil || !strings.Contains(err.Error(), "ANTIGRAVITY_CSRF_TOKEN") {
		t.Fatalf("error = %v", err)
	}
}
