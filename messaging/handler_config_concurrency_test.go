package messaging

import (
	"fmt"
	"sync"
	"testing"
)

const testSaveDirectoryIterations = 100

// TestSaveDirectoryConcurrentAccess 验证保存目录支持并发读取和更新。
func TestSaveDirectoryConcurrentAccess(t *testing.T) {
	h := NewHandler(nil, nil)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for index := 0; index < testSaveDirectoryIterations; index++ {
			h.SetSaveDir(fmt.Sprintf("/tmp/save-%d", index))
		}
	}()
	go func() {
		defer workers.Done()
		for index := 0; index < testSaveDirectoryIterations; index++ {
			_ = h.saveDirectory()
		}
	}()
	workers.Wait()
}
