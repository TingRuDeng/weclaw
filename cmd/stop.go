package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(stopCmd)
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "停止后台 WeClaw 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := stopAllWeclaw(); err != nil {
			return err
		}
		fmt.Println("WeClaw 已停止")
		return nil
	},
}
