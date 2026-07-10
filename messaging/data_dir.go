package messaging

import (
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/weclaw/config"
)

func defaultDataDir() string {
	dir, err := config.DataDir()
	if err != nil || dir == "" {
		return filepath.Join(os.TempDir(), "weclaw")
	}
	return dir
}
