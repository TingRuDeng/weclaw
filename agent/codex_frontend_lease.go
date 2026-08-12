package agent

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/fastclaw-ai/weclaw/config"
)

func codexRestartJournalPresent() (bool, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(filepath.Join(dataDir, "state", "runtime-restart.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
