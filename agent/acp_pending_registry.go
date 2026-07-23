package agent

import "sync"

// rpcPendingRegistry owns the lifecycle of in-flight JSON-RPC response
// waiters. It deliberately does not own request IDs, wire generations or
// transport writes; those remain ACPAgent responsibilities.
type rpcPendingRegistry struct {
	mu    sync.Mutex
	calls map[int64]chan *rpcResponse
}

func (r *rpcPendingRegistry) register(id int64) chan *rpcResponse {
	ch := make(chan *rpcResponse, 1)

	r.mu.Lock()
	if r.calls == nil {
		r.calls = make(map[int64]chan *rpcResponse)
	}
	r.calls[id] = ch
	r.mu.Unlock()

	return ch
}

func (r *rpcPendingRegistry) remove(id int64) {
	r.mu.Lock()
	delete(r.calls, id)
	r.mu.Unlock()
}

func (r *rpcPendingRegistry) deliver(response *rpcResponse) bool {
	if response == nil || response.ID == nil {
		return false
	}

	r.mu.Lock()
	ch := r.calls[*response.ID]
	r.mu.Unlock()
	if ch == nil {
		return false
	}

	select {
	case ch <- response:
		return true
	default:
		return false
	}
}

func (r *rpcPendingRegistry) failAll(reason string) {
	response := &rpcResponse{
		Error: &rpcError{Code: -32000, Message: reason},
	}

	r.mu.Lock()
	channels := make([]chan *rpcResponse, 0, len(r.calls))
	for id, ch := range r.calls {
		delete(r.calls, id)
		channels = append(channels, ch)
	}
	r.mu.Unlock()

	for _, ch := range channels {
		select {
		case ch <- response:
		default:
		}
	}
}

func (r *rpcPendingRegistry) contains(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.calls[id]
	return ok
}
