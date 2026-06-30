package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/fastclaw-ai/weclaw/web"
	"github.com/spf13/cobra"
)

var (
	webAddrFlag   string
	webTokenFlag  string
	webNoOpenFlag bool
)

func init() {
	webCmd.Flags().StringVar(&webAddrFlag, "addr", "127.0.0.1:39282", "Web config panel listen address")
	webCmd.Flags().StringVar(&webTokenFlag, "token", "", "Auth token (required when addr is not loopback)")
	webCmd.Flags().BoolVar(&webNoOpenFlag, "no-open", false, "Do not open the browser automatically")
	rootCmd.AddCommand(webCmd)
}

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Start the local web config panel",
	RunE:  runWeb,
}

func runWeb(cmd *cobra.Command, args []string) error {
	addr := strings.TrimSpace(webAddrFlag)
	token := strings.TrimSpace(webTokenFlag)
	if token == "" {
		token = generateWebToken()
	}

	srv := web.NewServer(web.Options{Addr: addr, Token: token})
	if err := srv.Validate(); err != nil {
		return err
	}

	url := fmt.Sprintf("http://%s/?token=%s", srv.Addr(), token)
	fmt.Printf("WeClaw 配置面板: %s\n", url)
	fmt.Println("按 Ctrl+C 停止。")
	if !webNoOpenFlag {
		openBrowser(url)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return srv.Run(ctx)
}

func generateWebToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "weclaw-web-token"
	}
	return hex.EncodeToString(b[:])
}

// openBrowser 尽力打开默认浏览器；失败不影响服务运行。
func openBrowser(url string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	cmd := exec.Command(name, args...)
	_ = cmd.Start()
}
