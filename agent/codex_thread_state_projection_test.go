package agent

import (
	"fmt"
	"testing"
)

func TestCodexThreadStateDoesNotReusePreviousTurnFinalText(t *testing.T) {
	state := codexThreadStateFromSnapshot(codexThreadSnapshot{
		ID: "thread-1",
		Turns: []codexTurnSnapshot{
			{ID: "turn-old", Status: "completed", Items: []codexThreadItem{
				{ID: "old-final", Type: "agentMessage", Phase: "final_answer", Text: "旧回答"},
			}},
			{ID: "turn-new", Status: "completed", Items: []codexThreadItem{
				{ID: "new-user", Type: "userMessage", Text: "新问题"},
			}},
		},
	})
	if state.LastTurnID != "turn-new" || state.LastAgentMessageText != "" {
		t.Fatalf("state=%#v, latest turn must not reuse the previous answer", state)
	}
}

func TestCodexDesktopStateUsesOnlyLatestTurnFinalAnswer(t *testing.T) {
	projection := codexDesktopProjectionState{
		order: []string{"turn-old", "turn-new"},
		turns: map[string]codexDesktopProjectedTurn{
			"turn-old": {
				id: "turn-old", status: "completed", order: []string{"old-final"},
				items: map[string]codexDesktopProjectedItem{
					"old-final": {id: "old-final", itemType: "agentMessage", phase: "final_answer", text: "旧回答"},
				},
			},
			"turn-new": {
				id: "turn-new", status: "completed", order: []string{"commentary"},
				items: map[string]codexDesktopProjectedItem{
					"commentary": {id: "commentary", itemType: "agentMessage", phase: "commentary", text: "处理中"},
				},
			},
		},
	}
	state := buildCodexDesktopThreadState(codexDesktopThreadStateSpec{
		threadID: "thread-1", raw: map[string]any{}, projection: projection,
	})
	if state.LastTurnID != "turn-new" || state.LastAgentMessageText != "" {
		t.Fatalf("state=%#v, commentary/previous answer must not become the new final", state)
	}
}

func TestCodexAppServerActiveTurnReplayShowsOnlyVisibleMessages(t *testing.T) {
	items := []codexThreadItem{
		{ID: "commentary", Type: "agentMessage", Phase: "commentary", Text: "正在复核当前实现。"},
		{ID: "command", Type: "commandExecution", Text: "不应展示的命令输出"},
		{ID: "reasoning", Type: "reasoning", Text: "不应展示的推理"},
		{ID: "final", Type: "agentMessage", Phase: "final_answer", Text: "最终回答不应进入进度"},
	}
	events := projectCodexAppServerActiveTurnEvents(codexThreadSnapshot{
		ID: "thread-1", Turns: []codexTurnSnapshot{{ID: "turn-1", Status: "inProgress", Items: items}},
	}, "turn-1")
	if len(events) != 3 {
		t.Fatalf("events=%#v, want commentary, hidden activity, and final candidate", events)
	}
	if events[0].Kind != "item_completed" || events[0].MessagePhase != "commentary" || events[0].Text != "正在复核当前实现。" {
		t.Fatalf("commentary=%#v", events[0])
	}
	if events[1].Kind != "activity" || events[1].Text != "" {
		t.Fatalf("command activity=%#v", events[1])
	}
	if events[2].Kind != "item_completed" || events[2].MessagePhase != "final_answer" {
		t.Fatalf("final candidate=%#v", events[2])
	}
}

func TestCodexAppServerActiveTurnReplayHasNoFixedEventLimit(t *testing.T) {
	items := make([]codexThreadItem, 300)
	for index := range items {
		items[index] = codexThreadItem{
			ID: fmt.Sprintf("commentary-%03d", index), Type: "agentMessage",
			Phase: "commentary", Text: fmt.Sprintf("进度 %03d", index),
		}
	}
	events := projectCodexAppServerActiveTurnEvents(codexThreadSnapshot{
		ID: "thread-1", Turns: []codexTurnSnapshot{{ID: "turn-1", Status: "inProgress", Items: items}},
	}, "turn-1")
	if len(events) != len(items) || events[len(events)-1].ItemID != "commentary-299" {
		t.Fatalf("replayed=%d last=%#v, want all %d messages", len(events), events[len(events)-1], len(items))
	}
}
