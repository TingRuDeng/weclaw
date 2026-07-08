package cmd

import "github.com/spf13/cobra"

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "管理本机配置",
}

// init 注册本机配置命令，避免把安全配置入口暴露到远程平台命令中。
func init() {
	configCmd.AddCommand(configPermissionCmd)
	rootCmd.AddCommand(configCmd)
}
