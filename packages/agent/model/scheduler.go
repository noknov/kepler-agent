package model

import (
	"context"
	"sync"
	"time"
)

type WorkClass string

const (
	WorkInteractive WorkClass = "interactive"
	WorkCompaction  WorkClass = "compaction"
	WorkBackground  WorkClass = "background"
)

type Workload struct {
	Owner string
	Class WorkClass
}
type workloadKey struct{}

// WithWorkload preserves the authenticated parent's owner across child work.
// Callers may change class, but children cannot create a new tenant identity.
func WithWorkload(ctx context.Context, workload Workload) context.Context {
	if parent, ok := ctx.Value(workloadKey{}).(Workload); ok && parent.Owner != "" {
		workload.Owner = parent.Owner
	}
	return context.WithValue(ctx, workloadKey{}, workload)
}
func workloadFrom(ctx context.Context) Workload {
	value, _ := ctx.Value(workloadKey{}).(Workload)
	if value.Owner == "" {
		value.Owner = "anonymous"
	}
	if value.Class == "" {
		value.Class = WorkInteractive
	}
	return value
}

type GovernorConfig struct{ Concurrency, MaxQueued int }
type Governor struct {
	mu                                     sync.Mutex
	concurrency, maxQueued, active, queued int
	owners                                 []string
	next                                   int
	queues                                 map[string]*ownerQueue
}
type ownerQueue struct {
	classes  []WorkClass
	next     int
	requests map[WorkClass][]*admission
}
type admission struct {
	ready    chan struct{}
	workload Workload
	granted  bool
	queuedAt time.Time
}
type GovernorSnapshot struct{ Active, Queued, Owners int }

func NewGovernor(concurrency int) *Governor {
	return NewGovernorWithConfig(GovernorConfig{Concurrency: concurrency})
}
func NewGovernorWithConfig(config GovernorConfig) *Governor {
	if config.Concurrency <= 0 {
		config.Concurrency = 16
	}
	if config.MaxQueued <= 0 {
		config.MaxQueued = 1024
	}
	return &Governor{concurrency: config.Concurrency, maxQueued: config.MaxQueued, queues: make(map[string]*ownerQueue)}
}
func (g *Governor) Snapshot() GovernorSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return GovernorSnapshot{g.active, g.queued, len(g.owners)}
}
func (g *Governor) acquire(ctx context.Context) (func(), time.Duration, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	work := workloadFrom(ctx)
	if work.Class != WorkInteractive && work.Class != WorkCompaction && work.Class != WorkBackground {
		return nil, 0, &Error{Kind: ErrorInvalid, Message: "unknown model workload class"}
	}
	request := &admission{ready: make(chan struct{}), workload: work, queuedAt: time.Now()}
	g.mu.Lock()
	if g.queued >= g.maxQueued {
		g.mu.Unlock()
		return nil, 0, &Error{Kind: ErrorBudgetExhausted, Message: "model admission queue is full"}
	}
	queue := g.queues[work.Owner]
	if queue == nil {
		queue = &ownerQueue{requests: make(map[WorkClass][]*admission)}
		g.queues[work.Owner] = queue
		g.owners = append(g.owners, work.Owner)
	}
	if _, exists := queue.requests[work.Class]; !exists {
		queue.classes = append(queue.classes, work.Class)
	}
	queue.requests[work.Class] = append(queue.requests[work.Class], request)
	g.queued++
	g.dispatchLocked()
	g.mu.Unlock()
	select {
	case <-request.ready:
		var once sync.Once
		release := func() { once.Do(func() { g.mu.Lock(); g.active--; g.dispatchLocked(); g.mu.Unlock() }) }
		if err := ctx.Err(); err != nil {
			release()
			return nil, time.Since(request.queuedAt), err
		}
		return release, time.Since(request.queuedAt), nil
	case <-ctx.Done():
		g.mu.Lock()
		if request.granted {
			g.active--
		} else {
			queue := g.queues[work.Owner]
			if queue != nil {
				list := queue.requests[work.Class]
				for i, candidate := range list {
					if candidate == request {
						queue.requests[work.Class] = append(list[:i], list[i+1:]...)
						g.queued--
						break
					}
				}
			}
		}
		g.dispatchLocked()
		g.mu.Unlock()
		return nil, time.Since(request.queuedAt), ctx.Err()
	}
}

// Each owner receives one admission per round; classes rotate inside an owner.
// FIFO applies inside each owner/class. Neither a new user nor a background
// class can continually jump ahead of an already queued peer.
func (g *Governor) dispatchLocked() {
	for g.active < g.concurrency && len(g.owners) > 0 {
		if g.next >= len(g.owners) {
			g.next = 0
		}
		owner := g.owners[g.next]
		queue := g.queues[owner]
		for i := len(queue.classes) - 1; i >= 0; i-- {
			class := queue.classes[i]
			if len(queue.requests[class]) == 0 {
				queue.classes = append(queue.classes[:i], queue.classes[i+1:]...)
				delete(queue.requests, class)
				if i < queue.next {
					queue.next--
				}
			}
		}
		if len(queue.classes) == 0 {
			delete(g.queues, owner)
			g.owners = append(g.owners[:g.next], g.owners[g.next+1:]...)
			continue
		}
		if queue.next >= len(queue.classes) {
			queue.next = 0
		}
		class := queue.classes[queue.next]
		list := queue.requests[class]
		request := list[0]
		queue.requests[class] = list[1:]
		queue.next = (queue.next + 1) % len(queue.classes)
		g.next = (g.next + 1) % len(g.owners)
		g.queued--
		g.active++
		request.granted = true
		close(request.ready)
	}
	// Remove empty owner queues even when all execution slots are occupied.
	// This also releases canceled queue state without waiting for another turn.
	for i := len(g.owners) - 1; i >= 0; i-- {
		owner := g.owners[i]
		queue := g.queues[owner]
		count := 0
		for _, list := range queue.requests {
			count += len(list)
		}
		if count == 0 {
			delete(g.queues, owner)
			g.owners = append(g.owners[:i], g.owners[i+1:]...)
			if i < g.next {
				g.next--
			}
		}
	}
	if g.next >= len(g.owners) {
		g.next = 0
	}
}
