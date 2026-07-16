package cmd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/fastclaw-ai/weclaw/web"
	"github.com/spf13/cobra"
)

var (
	webAddrFlag              string
	webTokenFlag             string
	webNoOpenFlag            bool
	webAllowInsecureHTTPFlag bool
)

// webTokenRandomReader 允许测试注入失败随机源，生产环境使用 crypto/rand。
var webTokenRandomReader io.Reader = rand.Reader

func init() {
	webCmd.Flags().StringVar(&webAddrFlag, "addr", "127.0.0.1:39282", "Web 配置面板监听地址")
	webCmd.Flags().StringVar(&webTokenFlag, "token", "", "访问 token（留空时自动生成）")
	webCmd.Flags().BoolVar(&webAllowInsecureHTTPFlag, "allow-insecure-http", false, "允许非本机明文 HTTP 监听（仅限可信网络）")
	webCmd.Flags().BoolVar(&webNoOpenFlag, "no-open", false, "启动后不自动打开浏览器")
	rootCmd.AddCommand(webCmd)
}

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "打开本地 Web 配置面板",
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

	srv := web.NewServer(web.Options{Addr: addr, Token: token, AllowInsecureHTTP: webAllowInsecureHTTPFlag})
	if err := srv.Validate(); err != nil {
		return err
	}

	panelURL := webPanelURL(srv.Addr(), token)
	fmt.Printf("WeClaw 配置面板: %s\n", panelURL)
	fmt.Println("按 Ctrl+C 停止。")
	if !webNoOpenFlag {
		openBrowser(panelURL)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return srv.Run(ctx)
}

// webPanelURL 把 token 放入 fragment；fragment 不会随 HTTP 请求发送到服务端或代理日志。
func webPanelURL(addr string, token string) string {
	fragment := url.Values{"token": {token}}.Encode()
	return (&url.URL{Scheme: "http", Host: addr, Path: "/", Fragment: fragment}).String()
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
