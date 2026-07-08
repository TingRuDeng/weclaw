package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "weclaw",
	Short: "WeClaw 连接微信、飞书和 AI Agent",
	Long:  "WeClaw 连接微信、飞书和 AI Agent，把聊天消息转给 Codex、Claude 等 Agent。",
	CompletionOptions: cobra.CompletionOptions{
		HiddenDefaultCmd: true,
	},
	Version: Version,
	RunE:    runStart, // default command is start
}

const weclawHelpTemplate = `{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}用法:
  {{.UseLine}}{{if .HasAvailableSubCommands}}

可用命令:
{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}  {{rpad .Name .NamePadding }} {{.Short}}
{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}
参数:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{if .HasAvailableInheritedFlags}}
全局参数:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{if .HasExample}}
示例:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}
使用 "{{.CommandPath}} [command] --help" 查看子命令帮助。{{end}}
`

func init() {
	rootCmd.SetHelpTemplate(weclawHelpTemplate)
	rootCmd.SetHelpCommand(&cobra.Command{
		Use:   "help [command]",
		Short: "查看命令帮助",
		Run: func(cmd *cobra.Command, args []string) {
			target, _, err := cmd.Root().Find(args)
			if err != nil || target == nil {
				target = cmd.Root()
			}
			_ = target.Help()
		},
	})
}

func prepareRootCommandHelp() {
	localizeHelpFlag(rootCmd)
	localizeVersionFlag(rootCmd)
}

func localizeHelpFlag(cmd *cobra.Command) {
	cmd.InitDefaultHelpFlag()
	if flag := cmd.Flags().Lookup("help"); flag != nil {
		flag.Usage = "查看帮助"
	}
	for _, child := range cmd.Commands() {
		localizeHelpFlag(child)
	}
}

func localizeVersionFlag(cmd *cobra.Command) {
	cmd.InitDefaultVersionFlag()
	if flag := cmd.Flags().Lookup("version"); flag != nil {
		flag.Usage = "查看版本"
	}
}

// Execute runs the root command.
func Execute() {
	prepareRootCommandHelp()
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
