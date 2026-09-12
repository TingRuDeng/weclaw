// 仅供离线迁移测试启动隔离的旧协议服务，不创建任何 Codex Host。
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "start" {
		panic("expected start")
	}
	dir := os.Getenv("WECLAW_HOME")
	if dir == "" {
		panic("missing isolated home")
	}
	lock, err := os.OpenFile(filepath.Join(dir, "weclaw.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		panic(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		panic(err)
	}
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	data, err := json.Marshal(map[string]any{"pid": os.Getpid(), "exe": exe, "version": "v0.1.245", "mode": "foreground", "started_at": time.Now()})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "weclaw.pid"), data, 0600); err != nil {
		panic(err)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() { _ = http.Serve(listener, http.NotFoundHandler()) }()
	fmt.Println(listener.Addr().String())
	<-signals
}
