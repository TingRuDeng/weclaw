package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
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

// webTokenRandomReader 允许测试注入失败随机源，生产环境使用 crypto/rand。
var webTokenRandomReader io.Reader = rand.Reader

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
		generated, err := generateWebToken()
		if err != nil {
			return fmt.Errorf("生成 Web 认证 token 失败: %w", err)
		}
		token = generated
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

// generateWebToken 生成 Web 面板认证 token；随机源失败时返回错误，避免固定 token 降级。
func generateWebToken() (string, error) {
	var b [24]byte
	if _, err := io.ReadFull(webTokenRandomReader, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
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
