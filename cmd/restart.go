package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the background weclaw process",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Stopping weclaw...")
		if err := stopAllWeclaw(); err != nil {
			return err
		}
		fmt.Println("Starting weclaw...")
		return runDaemon()
	},
}
