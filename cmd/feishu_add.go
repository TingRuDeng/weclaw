package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const feishuAddDefaultProgressMode = "stream"

type feishuAddOptions struct {
	Name                  string
	DisplayName           string
	DisplayNameSet        bool
	Aliases               []string
	AliasesSet            bool
	AppID                 string
	AppSecret             string
	AllowedUsers          []string
	AllowedUsersSet       bool
	DefaultAgent          string
	DefaultAgentSet       bool
	ProgressMode          string
	ProgressModeSet       bool
	RequireMentionInGroup *bool
}

type feishuAddPrompter interface {
	Prompt(label string, defaultValue string) (string, error)
	PromptSecret(label string) (string, error)
	PromptBool(label string, defaultValue bool) (bool, error)
}

var feishuAddCmd = &cobra.Command{
	Use:   "add",
	Short: "交互式添加飞书机器人",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		return runFeishuAdd(ctx, feishuAddOptions{
			Name:                  feishuBotName,
			DisplayName:           feishuBotDisplayName,
			DisplayNameSet:        cmd.Flags().Changed("display-name"),
			Aliases:               splitCSV(feishuBotAliases),
			AliasesSet:            cmd.Flags().Changed("aliases"),
			AppID:                 feishuLoginAppID,
			AppSecret:             feishuLoginAppSecret,
			AllowedUsers:          splitCSV(feishuBootstrapAllowedUsers),
			AllowedUsersSet:       cmd.Flags().Changed("allowed-users"),
			DefaultAgent:          feishuBootstrapDefaultAgent,
			DefaultAgentSet:       cmd.Flags().Changed("default-agent"),
			ProgressMode:          feishuBootstrapProgressMode,
			ProgressModeSet:       cmd.Flags().Changed("progress"),
			RequireMentionInGroup: feishuAddBoolFromFlag(cmd.Flags().Changed("require-mention-in-group"), feishuBootstrapRequireMention),
		}, newTerminalFeishuAddPrompter(os.Stdin, os.Stdout))
	},
}

type terminalFeishuAddPrompter struct {
	reader   *bufio.Reader
	out      io.Writer
	secretFD int
}

// newTerminalFeishuAddPrompter 构造真实终端输入器，用 stdin 读取答案并向 stdout 输出提示。
func newTerminalFeishuAddPrompter(in *os.File, out io.Writer) *terminalFeishuAddPrompter {
	return &terminalFeishuAddPrompter{
		reader:   bufio.NewReader(in),
		out:      out,
		secretFD: int(in.Fd()),
	}
}

// runFeishuAdd 收集交互输入后复用 bootstrap，确保凭证和配置只有一条落盘路径。
func runFeishuAdd(ctx context.Context, opts feishuAddOptions, prompter feishuAddPrompter) error {
	if prompter == nil {
		return fmt.Errorf("飞书交互输入器不能为空")
	}
	bootstrapOpts, err := collectFeishuAddOptions(opts, prompter)
	if err != nil {
		return err
	}
	return runFeishuBootstrap(ctx, bootstrapOpts)
}

// collectFeishuAddOptions 只补齐命令行未提供的字段，避免覆盖脚本传入的显式配置。
func collectFeishuAddOptions(opts feishuAddOptions, prompter feishuAddPrompter) (feishuBootstrapOptions, error) {
	var err error
	if opts.Name, err = promptFeishuAddString(prompter, "Bot 内部 ID（仅英文、数字、点、横线、下划线）", opts.Name); err != nil {
		return feishuBootstrapOptions{}, err
	}
	if !opts.DisplayNameSet {
		opts.DisplayName, err = prompter.Prompt("Bot 展示名（主显示名称，可填中文）", opts.DisplayName)
		if err != nil {
			return feishuBootstrapOptions{}, err
		}
	}
	if !opts.AliasesSet {
		opts.Aliases, err = promptFeishuAddAliases(prompter)
		if err != nil {
			return feishuBootstrapOptions{}, err
		}
	}
	if opts.AppID, err = promptFeishuAddString(prompter, "飞书 app_id", opts.AppID); err != nil {
		return feishuBootstrapOptions{}, err
	}
	if strings.TrimSpace(opts.AppSecret) == "" {
		if opts.AppSecret, err = prompter.PromptSecret("飞书 app_secret"); err != nil {
			return feishuBootstrapOptions{}, err
		}
	}
	if !opts.AllowedUsersSet {
		opts.AllowedUsers, err = promptFeishuAddCSV(prompter)
		if err != nil {
			return feishuBootstrapOptions{}, err
		}
	}
	if !opts.DefaultAgentSet {
		opts.DefaultAgent, err = prompter.Prompt("默认 Agent，可留空", "")
		if err != nil {
			return feishuBootstrapOptions{}, err
		}
	}
	if !opts.ProgressModeSet {
		opts.ProgressMode, err = prompter.Prompt("进度模式 off/typing/summary/verbose/stream/debug", feishuAddDefaultProgressMode)
		if err != nil {
			return feishuBootstrapOptions{}, err
		}
	}
	if opts.RequireMentionInGroup == nil {
		mention, err := prompter.PromptBool("群聊中是否要求 @ 机器人", true)
		if err != nil {
			return feishuBootstrapOptions{}, err
		}
		opts.RequireMentionInGroup = &mention
	}
	return opts.toBootstrapOptions(), nil
}

// toBootstrapOptions 转成现有 bootstrap 入参，确保后续校验和落盘复用同一实现。
func (opts feishuAddOptions) toBootstrapOptions() feishuBootstrapOptions {
	return feishuBootstrapOptions{
		Name:                  opts.Name,
		DisplayName:           opts.DisplayName,
		Aliases:               opts.Aliases,
		AppID:                 opts.AppID,
		AppSecret:             opts.AppSecret,
		AllowedUsers:          opts.AllowedUsers,
		DefaultAgent:          opts.DefaultAgent,
		ProgressMode:          opts.ProgressMode,
		RequireMentionInGroup: opts.RequireMentionInGroup,
	}
}

// promptFeishuAddAliases 读取飞书机器人别名，空输入表示不配置别名。
func promptFeishuAddAliases(prompter feishuAddPrompter) ([]string, error) {
	value, err := prompter.Prompt("Bot 别名（额外可识别名称，逗号分隔，可留空）", "")
	if err != nil {
		return nil, err
	}
	return splitCSV(value), nil
}

// promptFeishuAddString 在已有值为空时才询问用户，支持命令行参数覆盖交互输入。
func promptFeishuAddString(prompter feishuAddPrompter, label string, value string) (string, error) {
	if strings.TrimSpace(value) != "" {
		return value, nil
	}
	return prompter.Prompt(label, "")
}

// promptFeishuAddCSV 读取并解析飞书用户白名单，空输入表示不限制在配置层写入名单。
func promptFeishuAddCSV(prompter feishuAddPrompter) ([]string, error) {
	value, err := prompter.Prompt("允许使用的飞书用户 open_id/union_id，逗号分隔，可留空", "")
	if err != nil {
		return nil, err
	}
	return splitCSV(value), nil
}

// feishuAddBoolFromFlag 只在用户显式传 flag 时返回指针，保留交互确认默认行为。
func feishuAddBoolFromFlag(changed bool, value bool) *bool {
	if !changed {
		return nil
	}
	return &value
}

// Prompt 输出普通文本问题；空答案会采用默认值。
func (p *terminalFeishuAddPrompter) Prompt(label string, defaultValue string) (string, error) {
	prompt := label
	if defaultValue != "" {
		prompt = fmt.Sprintf("%s [%s]", label, defaultValue)
	}
	if _, err := fmt.Fprintf(p.out, "%s: ", prompt); err != nil {
		return "", err
	}
	value, err := p.readLine()
	if err != nil {
		return "", err
	}
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

// PromptSecret 在真实 TTY 中隐藏 app_secret，非 TTY 场景则按行读取以支持管道输入。
func (p *terminalFeishuAddPrompter) PromptSecret(label string) (string, error) {
	if _, err := fmt.Fprintf(p.out, "%s: ", label); err != nil {
		return "", err
	}
	if term.IsTerminal(p.secretFD) {
		value, err := term.ReadPassword(p.secretFD)
		_, _ = fmt.Fprintln(p.out)
		return strings.TrimSpace(string(value)), err
	}
	return p.readLine()
}

// PromptBool 循环读取布尔答案，避免把无效输入写入配置。
func (p *terminalFeishuAddPrompter) PromptBool(label string, defaultValue bool) (bool, error) {
	for {
		value, err := p.Prompt(label+" "+boolPromptSuffix(defaultValue), "")
		if err != nil {
			return false, err
		}
		parsed, ok := parseFeishuAddBool(value, defaultValue)
		if ok {
			return parsed, nil
		}
		if _, err := fmt.Fprintln(p.out, "请输入 y 或 n。"); err != nil {
			return false, err
		}
	}
}

// readLine 兼容最后一行没有换行符的管道输入。
func (p *terminalFeishuAddPrompter) readLine() (string, error) {
	line, err := p.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// boolPromptSuffix 按默认值显示常见的 y/n 交互提示。
func boolPromptSuffix(defaultValue bool) string {
	if defaultValue {
		return "[Y/n]"
	}
	return "[y/N]"
}

// parseFeishuAddBool 支持中英文和常见布尔输入，减少终端交互误差。
func parseFeishuAddBool(value string, defaultValue bool) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return defaultValue, true
	case "y", "yes", "true", "1", "是":
		return true, true
	case "n", "no", "false", "0", "否":
		return false, true
	default:
		return false, false
	}
}
