package agent

import "testing"

// TestTurnChannelRegistrationRejectsSecondOwner 验证同一真实会话不能覆盖已有事件接收者。
func TestTurnChannelRegistrationRejectsSecondOwner(t *testing.T) {
	a := &ACPAgent{turnCh: make(map[string]chan *codexTurnEvent)}
	first := make(chan *codexTurnEvent, 1)
	second := make(chan *codexTurnEvent, 1)

	if !a.registerTurnChannel("thread-1", first) {
		t.Fatal("first registration rejected")
	}
	if a.registerTurnChannel("thread-1", second) {
		t.Fatal("second registration replaced active owner")
	}
	if got := a.turnCh["thread-1"]; got != first {
		t.Fatalf("registered channel = %p, want first owner %p", got, first)
	}
}

// TestTurnChannelRegistrationOnlyOwnerCanUnregister 验证旧任务不能删除后来注册的通道。
func TestTurnChannelRegistrationOnlyOwnerCanUnregister(t *testing.T) {
	a := &ACPAgent{turnCh: make(map[string]chan *codexTurnEvent)}
	owner := make(chan *codexTurnEvent, 1)
	other := make(chan *codexTurnEvent, 1)

	if !a.registerTurnChannel("thread-1", owner) {
		t.Fatal("owner registration rejected")
	}
	a.unregisterTurnChannel("thread-1", other)
	if got := a.turnCh["thread-1"]; got != owner {
		t.Fatalf("non-owner removed channel, got %p", got)
	}

	a.unregisterTurnChannel("thread-1", owner)
	if _, ok := a.turnCh["thread-1"]; ok {
		t.Fatal("owner channel was not removed")
	}
}
