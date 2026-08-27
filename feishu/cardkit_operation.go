package feishu

import (
	"strings"
	"sync"
)

// cardKitOperationCoordinator 串行化同一张 CardKit 卡片的完整操作。
//
// CardKit 的 sequence 语义要求“分配序号”和“发出请求”保持同一顺序。
// 仅保护本地 registry 会让较早的请求在网络层被较晚的请求越过，最终触发
// 300317 sequence number compare failed。按 card_id 分片可以保留不同卡片
// 之间的并行，同时避免为每张历史卡片永久保留一个 mutex。
type cardKitOperationCoordinator struct {
	mu      sync.Mutex
	entries map[string]*cardKitOperationEntry
}

type cardKitOperationEntry struct {
	mu   sync.Mutex
	refs int
}

func newCardKitOperationCoordinator() *cardKitOperationCoordinator {
	return &cardKitOperationCoordinator{entries: make(map[string]*cardKitOperationEntry)}
}

func (c *cardKitOperationCoordinator) with(cardID string, fn func() error) error {
	if fn == nil {
		return nil
	}
	cardID = strings.TrimSpace(cardID)
	if cardID == "" {
		return fn()
	}
	if c == nil {
		return fn()
	}

	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*cardKitOperationEntry)
	}
	entry := c.entries[cardID]
	if entry == nil {
		entry = &cardKitOperationEntry{}
		c.entries[cardID] = entry
	}
	entry.refs++
	c.mu.Unlock()

	entry.mu.Lock()
	defer func() {
		entry.mu.Unlock()
		c.mu.Lock()
		entry.refs--
		if entry.refs == 0 && c.entries[cardID] == entry {
			delete(c.entries, cardID)
		}
		c.mu.Unlock()
	}()
	return fn()
}
