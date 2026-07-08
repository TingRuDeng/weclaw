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
	Short: "检查运行状态",
	RunE: func(cmd *cobra.Command, args []string) error {
		state, err := readRuntimeState()
		if err != nil {
			fmt.Println("WeClaw 未运行")
			return nil
		}

		if processExists(state.PID) {
			fmt.Printf("WeClaw 运行中 (pid=%d)\n", state.PID)
			if state.Exe != "" {
				fmt.Printf("路径: %s\n", state.Exe)
			}
			if state.Mode != "" {
				fmt.Printf("模式: %s\n", state.Mode)
			}
			fmt.Printf("日志: %s\n", logFile())
		} else {
			fmt.Println("WeClaw 未运行（存在过期 pid 文件）")
		}
		return nil
	},
}
