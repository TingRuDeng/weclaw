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
	sendCmd.Flags().StringVar(&sendTo, "to", "", "Target user ID (ilink user ID)")
	sendCmd.Flags().StringVar(&sendText, "text", "", "Message text to send")
	sendCmd.Flags().StringVar(&sendMediaURL, "media", "", "Media URL to send (image/video/file)")
	sendCmd.MarkFlagRequired("to")
	rootCmd.AddCommand(sendCmd)
}

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a message to a WeChat user",
	Example: `  weclaw send --to "user_id@im.wechat" --text "Hello"
  weclaw send --to "user_id@im.wechat" --media "https://example.com/image.png"
  weclaw send --to "user_id@im.wechat" --text "See this" --media "https://example.com/image.png"`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if sendText == "" && sendMediaURL == "" {
			return fmt.Errorf("at least one of --text or --media is required")
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		accounts, err := ilink.LoadAllCredentials()
		if err != nil {
			return fmt.Errorf("load credentials: %w", err)
		}
		if len(accounts) == 0 {
			return fmt.Errorf("no accounts found, run 'weclaw start' first")
		}

		client := ilink.NewClient(accounts[0])
		reply := wechat.NewReplier(client, sendTo, "", "")

		if sendText != "" {
			if err := reply.SendText(ctx, sendText); err != nil {
				return fmt.Errorf("send text failed: %w", err)
			}
			fmt.Println("Text sent")
		}

		if sendMediaURL != "" {
			if err := reply.SendMediaFromURL(ctx, sendMediaURL); err != nil {
				return fmt.Errorf("send media failed: %w", err)
			}
			fmt.Println("Media sent")
		}

		return nil
	},
}
