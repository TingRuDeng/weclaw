package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const codexInterruptTimeout = 5 * time.Second

func codexTurnIDFromStartResult(result json.RawMessage) string {
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if json.Unmarshal(result, &response) != nil {
		return ""
	}
	return strings.TrimSpace(response.Turn.ID)
}

// interruptCancelledCodexTurn 使用独立 context 中断远端 turn，避免本地取消只停止监听。
func (a *ACPAgent) interruptCancelledCodexTurn(threadID string, turnID string, turnIDCh <-chan string) error {
	if strings.TrimSpace(turnID) == "" {
		select {
		case turnID = <-turnIDCh:
		case <-time.After(codexInterruptTimeout):
			return fmt.Errorf("turn id unavailable after cancellation")
		}
	}
	interruptCtx, cancel := context.WithTimeout(context.Background(), codexInterruptTimeout)
	defer cancel()
	return a.InterruptCodexThread(interruptCtx, "", threadID, turnID)
}
