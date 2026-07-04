package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check if weclaw is running in background",
	RunE: func(cmd *cobra.Command, args []string) error {
		state, err := readRuntimeState()
		if err != nil {
			fmt.Println("weclaw is not running")
			return nil
		}

		if processExists(state.PID) {
			fmt.Printf("weclaw is running (pid=%d)\n", state.PID)
			if state.Exe != "" {
				fmt.Printf("Path: %s\n", state.Exe)
			}
			if state.Mode != "" {
				fmt.Printf("Mode: %s\n", state.Mode)
			}
			fmt.Printf("Log: %s\n", logFile())
		} else {
			fmt.Println("weclaw is not running (stale pid file)")
		}
		return nil
	},
}
