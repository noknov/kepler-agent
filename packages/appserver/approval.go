package appserver

import (
	"context"
	"fmt"
	"sync"
)

// ApprovalBroker connects a local UI's approval action to an in-flight agent
// tool call. It intentionally stores only live waiters: durable approval rules
// belong to the local profile's ScopedApprover.
type ApprovalBroker struct {
	mu      sync.Mutex
	pending map[string]chan string
}

func NewApprovalBroker() *ApprovalBroker {
	return &ApprovalBroker{pending: make(map[string]chan string)}
}

// Wait blocks the active turn until its specific tool call is approved or
// denied by the app-server client.
func (b *ApprovalBroker) Wait(ctx context.Context, turnID, toolCallID string) (string, error) {
	key := approvalID(turnID, toolCallID)
	answer := make(chan string, 1)
	b.mu.Lock()
	if _, exists := b.pending[key]; exists {
		b.mu.Unlock()
		return "", fmt.Errorf("approval already pending for tool call")
	}
	b.pending[key] = answer
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, key)
		b.mu.Unlock()
	}()
	select {
	case scope := <-answer:
		return scope, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Resolve supplies the scope selected by the user. A resolution is accepted
// only while the matching call is waiting, preventing stale UI actions from
// authorizing a later tool call.
func (b *ApprovalBroker) Resolve(turnID, toolCallID, scope string) error {
	key := approvalID(turnID, toolCallID)
	b.mu.Lock()
	answer := b.pending[key]
	b.mu.Unlock()
	if answer == nil {
		return fmt.Errorf("approval request not found")
	}
	select {
	case answer <- scope:
		return nil
	default:
		return fmt.Errorf("approval already resolved")
	}
}

func approvalID(turnID, toolCallID string) string { return turnID + "\x00" + toolCallID }
