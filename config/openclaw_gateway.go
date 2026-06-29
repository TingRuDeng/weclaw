package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// loadOpenclawGateway 解析 openclaw gateway 连接信息，优先环境变量，其次本地配置文件。
func loadOpenclawGateway() (gwURL, gwToken, gwPassword string) {
	gwURL = os.Getenv("OPENCLAW_GATEWAY_URL")
	gwToken = os.Getenv("OPENCLAW_GATEWAY_TOKEN")
	gwPassword = os.Getenv("OPENCLAW_GATEWAY_PASSWORD")
	if gwURL != "" {
		return
	}
	cfg, ok := readOpenclawGatewayConfig()
	if !ok {
		return
	}
	return resolveOpenclawGateway(cfg)
}

type openclawGatewayConfig struct {
	Gateway struct {
		Port int    `json:"port"`
		Mode string `json:"mode"`
		Auth struct {
			Mode     string `json:"mode"`
			Token    string `json:"token"`
			Password string `json:"password"`
		} `json:"auth"`
		Remote struct {
			URL   string `json:"url"`
			Token string `json:"token"`
		} `json:"remote"`
	} `json:"gateway"`
}

func readOpenclawGatewayConfig() (openclawGatewayConfig, bool) {
	var cfg openclawGatewayConfig
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg, false
	}
	data, err := os.ReadFile(filepath.Join(home, ".openclaw", "openclaw.json"))
	if err != nil {
		return cfg, false
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[config] failed to parse openclaw config: %v", err)
		return cfg, false
	}
	return cfg, true
}

func resolveOpenclawGateway(cfg openclawGatewayConfig) (gwURL, gwToken, gwPassword string) {
	gw := cfg.Gateway
	if gw.Remote.URL != "" {
		return gw.Remote.URL, gw.Remote.Token, ""
	}
	if gw.Port <= 0 {
		return "", "", ""
	}
	gwURL = fmt.Sprintf("ws://127.0.0.1:%d", gw.Port)
	switch gw.Auth.Mode {
	case "token":
		gwToken = gw.Auth.Token
	case "password":
		gwPassword = gw.Auth.Password
	}
	return gwURL, gwToken, gwPassword
}
