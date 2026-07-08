package cmd

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/wechat"
	"github.com/spf13/cobra"
)

var (
	sendTo       string
	sendText     string
	sendMediaURL string
)

func init() {
	sendCmd.Flags().StringVar(&sendTo, "to", "", "接收方用户 ID，例如 user_id@im.wechat")
	sendCmd.Flags().StringVar(&sendText, "text", "", "要发送的文本")
	sendCmd.Flags().StringVar(&sendMediaURL, "media", "", "要发送的图片、视频或文件 URL")
	sendCmd.MarkFlagRequired("to")
	rootCmd.AddCommand(sendCmd)
}

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "发送微信消息",
	Example: `  weclaw send --to "user_id@im.wechat" --text "Hello"
  weclaw send --to "user_id@im.wechat" --media "https://example.com/image.png"
  weclaw send --to "user_id@im.wechat" --text "See this" --media "https://example.com/image.png"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if sendText == "" && sendMediaURL == "" {
			return fmt.Errorf("必须提供 --text 或 --media")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		accounts, err := ilink.LoadAllCredentials()
		if err != nil {
			return fmt.Errorf("load credentials: %w", err)
		}
		if len(accounts) == 0 {
			return fmt.Errorf("未找到微信账号，请先运行 weclaw start 或 weclaw login")
		}

		client := ilink.NewClient(accounts[0])
		reply := wechat.NewReplier(client, sendTo, "", "")

		if sendText != "" {
			if err := reply.SendText(ctx, sendText); err != nil {
				return fmt.Errorf("发送文本失败: %w", err)
			}
			fmt.Println("文本已发送")
		}

		if sendMediaURL != "" {
			if err := reply.SendMediaFromURL(ctx, sendMediaURL); err != nil {
				return fmt.Errorf("发送媒体失败: %w", err)
			}
			fmt.Println("媒体已发送")
		}

		return nil
	},
}
