package messaging

import (
	"context"
	"strings"
	"testing"
)

func TestTextDedupOnlyBlocksMatchingActiveTask(t *testing.T) {
	h := NewHandler(nil, nil)
	if h.isDuplicateTextMessage("user-1", "ctx-1", "route-1", "继续") {
		t.Fatal("首次消息不应判重")
	}
	if h.isDuplicateTextMessage("user-1", "ctx-1", "route-1", "继续") {
		t.Fatal("没有活动任务时，合法重复消息不应被吞")
	}
	task, _, started := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", message: "继续",
	})
	if !started {
		t.Fatal("测试任务启动失败")
	}
	if !h.isDuplicateTextMessage("user-1", "ctx-1", "route-1", "继续") {
		t.Fatal("对应任务运行中时应拦截重复投递")
	}
	h.finishActiveTask("task-1", task)
	if h.isDuplicateTextMessage("user-1", "ctx-1", "route-1", "继续") {
		t.Fatal("任务结束后应允许再次发送相同文本")
	}
}

func TestTextDedupDoesNotCrossRoutesOrTruncatedPrefixes(t *testing.T) {
	h := NewHandler(nil, nil)
	first := strings.Repeat("甲", pendingCodexPreviewRunes) + "A"
	second := strings.Repeat("甲", pendingCodexPreviewRunes) + "B"
	if h.isDuplicateTextMessage("user-1", "ctx-1", "route-1", first) {
		t.Fatal("首次消息不应判重")
	}
	task, _, _ := h.beginActiveTask(context.Background(), "task-1", activeTaskMeta{
		owner: "user-1", routeUserID: "route-1", message: first,
	})
	defer h.finishActiveTask("task-1", task)
	if h.isDuplicateTextMessage("user-1", "ctx-1", "route-2", first) {
		t.Fatal("不同 route 的相同文本不应判重")
	}
	if h.isDuplicateTextMessage("user-1", "ctx-1", "route-1", second) {
		t.Fatal("仅截断预览相同的长文本不应判重")
	}
}
