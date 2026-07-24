package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/weclaw/messaging"
	"github.com/spf13/cobra"
)

var outboxStatusJSON bool
var outboxRedriveJSON bool

var outboxCmd = &cobra.Command{
	Use:   "outbox",
	Short: "查看和重投终态消息 outbox",
}

var outboxStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看终态消息积压和重试状态",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, online, err := loadCodexAccountRuntime()
		if err != nil {
			return err
		}
		var status messaging.TerminalOutboxStatus
		if online {
			if err := callCodexAccountAPI(cmd.Context(), cfg, http.MethodGet, "/api/terminal-outbox", nil, &status); err != nil {
				return fmt.Errorf("WeClaw 服务正在运行，但本机 outbox API 不可用: %w", err)
			}
		} else {
			status, err = messaging.InspectTerminalOutbox(messaging.DefaultTerminalOutboxFile())
			if err != nil {
				return fmt.Errorf("读取本地终态 outbox: %w", err)
			}
		}
		return writeTerminalOutboxStatus(cmd, status, outboxStatusJSON)
	},
}

var outboxRedriveCmd = &cobra.Command{
	Use:   "redrive [entry-id]",
	Short: "立即重投指定或全部待处理终态消息",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, online, err := loadCodexAccountRuntime()
		if err != nil {
			return err
		}
		if !online {
			return fmt.Errorf("WeClaw 服务未运行，无法安全重投；请先启动服务")
		}
		id := ""
		if len(args) == 1 {
			id = strings.TrimSpace(args[0])
		}
		var result messaging.TerminalOutboxRedriveResult
		if err := callCodexAccountAPI(cmd.Context(), cfg, http.MethodPost, "/api/terminal-outbox/redrive", map[string]string{"id": id}, &result); err != nil {
			return fmt.Errorf("终态 outbox 重投失败: %w", err)
		}
		if outboxRedriveJSON {
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(result)
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "已唤醒 %d 条终态投递。\n", result.Requested); err != nil {
			return err
		}
		return writeTerminalOutboxStatus(cmd, result.Status, false)
	},
}

func init() {
	rootCmd.AddCommand(outboxCmd)
	outboxCmd.AddCommand(outboxStatusCmd, outboxRedriveCmd)
	outboxStatusCmd.Flags().BoolVar(&outboxStatusJSON, "json", false, "输出 JSON")
	outboxRedriveCmd.Flags().BoolVar(&outboxRedriveJSON, "json", false, "输出 JSON")
}

func writeTerminalOutboxStatus(cmd *cobra.Command, status messaging.TerminalOutboxStatus, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	}
	if status.Pending == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "终态 outbox 无积压。")
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "待投递: %d（准备中 %d，投递中 %d）\n", status.Pending, status.Preparing, status.Processing); err != nil {
		return err
	}
	if !status.OldestCreatedAt.IsZero() {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "最早积压: %s（%s）\n",
			status.OldestCreatedAt.Local().Format("2006-01-02 15:04:05"), humanOutboxAge(time.Since(status.OldestCreatedAt))); err != nil {
			return err
		}
	}
	if !status.NextAttempt.IsZero() {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "下次重试: %s\n", status.NextAttempt.Local().Format("2006-01-02 15:04:05")); err != nil {
			return err
		}
	}
	if status.RecentError != "" {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "最近错误: %s\n", status.RecentError); err != nil {
			return err
		}
	}
	for _, entry := range status.Entries {
		state := "等待重试"
		if entry.Preparing {
			state = "准备中"
		} else if entry.Processing {
			state = "投递中"
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "- %s  %s  attempts=%d  next=%s\n",
			entry.ID, state, entry.Attempts, entry.NextAttempt.Local().Format("2006-01-02 15:04:05")); err != nil {
			return err
		}
	}
	if status.Truncated {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "仅显示最早的 100 条；使用 --json 获取相同的脱敏窗口。")
		return err
	}
	return nil
}

func humanOutboxAge(age time.Duration) string {
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return "不足 1 分钟"
	case age < time.Hour:
		return fmt.Sprintf("%d 分钟", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(age/time.Hour))
	default:
		return fmt.Sprintf("%d 天", int(age/(24*time.Hour)))
	}
}
