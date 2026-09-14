package cmd

import "testing"

func TestRootCommandDoesNotDuplicateCobraErrors(t *testing.T) {
	if !rootCmd.SilenceUsage || !rootCmd.SilenceErrors {
		t.Fatalf("SilenceUsage=%v SilenceErrors=%v, want both true", rootCmd.SilenceUsage, rootCmd.SilenceErrors)
	}
}
