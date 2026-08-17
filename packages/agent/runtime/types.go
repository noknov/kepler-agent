// Package runtime implements the transport-neutral agent execution loop.
package runtime

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/environment"
	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/agent/prompt"
	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

type TerminationReason string

const (
	TerminationCompleted       TerminationReason = "completed"
	TerminationCanceled        TerminationReason = "canceled"
	TerminationModelError      TerminationReason = "model_error"
	TerminationMaxSteps        TerminationReason = "max_steps"
	TerminationOutputLimit     TerminationReason = "output_limit"
	TerminationPendingApproval TerminationReason = "pending_approval"
	TerminationPendingInput    TerminationReason = "pending_input"
	TerminationEmptyResponse   TerminationReason = "empty_response"
)

type Config struct {
	Model           string
	ReasoningEffort string
	Temperature     float64
	MaxOutputTokens int
	MaxSteps        int
	MaxModelRetries int
	RetryBaseDelay  time.Duration
	Context         ContextConfig
	ToolResults     ToolResultConfig
	CircuitBreaker  CircuitBreakerConfig
}

func (c Config) withDefaults() Config {
	if c.MaxSteps <= 0 {
		c.MaxSteps = 32
	}
	if c.MaxModelRetries < 0 {
		c.MaxModelRetries = 0
	}
	if c.RetryBaseDelay <= 0 {
		c.RetryBaseDelay = 500 * time.Millisecond
	}
	if c.Context.MaxTokens <= 0 {
		c.Context.MaxTokens = 96_000
	}
	if c.Context.ReserveTokens <= 0 {
		c.Context.ReserveTokens = 8_000
	}
	if c.ToolResults.MaxInlineBytes <= 0 {
		c.ToolResults.MaxInlineBytes = 64 << 10
	}
	if c.ToolResults.MaxBatchBytes <= 0 {
		c.ToolResults.MaxBatchBytes = c.ToolResults.MaxInlineBytes
	}
	return c
}

type Dependencies struct {
	Model       model.Client
	Tools       *tool.Catalog
	Policy      tool.Policy
	Approver    tool.Approver
	Transcript  transcript.Store
	Events      transcript.Sink
	Projector   Projector
	Compactor   Compactor
	Environment environment.Config
	Artifacts   ArtifactStore
	IDs         IDGenerator
	Clock       func() time.Time
	Sleep       func(context.Context, time.Duration) error
}

type Runtime struct {
	config Config
	deps   Dependencies
	lockMu sync.Mutex
	locks  map[string]*sessionMutex
}

type sessionMutex struct {
	mu   sync.Mutex
	refs int
}

func New(config Config, deps Dependencies) (*Runtime, error) {
	if deps.Model == nil {
		return nil, errors.New("model client is required")
	}
	if deps.Tools == nil {
		return nil, errors.New("tool catalog is required")
	}
	if deps.Transcript == nil {
		return nil, errors.New("transcript store is required")
	}
	if deps.Policy == nil {
		deps.Policy = tool.ReadOnlyPolicy{}
	}
	if deps.IDs == nil {
		deps.IDs = RandomIDs{}
	}
	if deps.Clock == nil {
		deps.Clock = func() time.Time { return time.Now().UTC() }
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepContext
	}
	config = config.withDefaults()
	if deps.Projector == nil {
		deps.Projector = NewBoundedProjector(config.Context)
	}
	return &Runtime{config: config, deps: deps, locks: make(map[string]*sessionMutex)}, nil
}

type InputSource interface {
	Claim(context.Context, int) ([]PendingInput, error)
	Ack(context.Context, string) error
}

type PendingInput struct {
	ID      string
	Message model.Message
}

// InputBuffer is a concurrency-safe steering inbox shared by interactive
// surfaces and the runtime. Drain atomically transfers all pending messages.
type InputBuffer struct {
	mu       sync.Mutex
	messages []model.Message
	nextID   uint64
	closed   bool
}

func (b *InputBuffer) Push(message model.Message) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.messages = append(b.messages, message)
	return true
}

func (b *InputBuffer) Drain() []model.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	messages := append([]model.Message(nil), b.messages...)
	b.messages = b.messages[:0]
	return messages
}

func (b *InputBuffer) Claim(_ context.Context, limit int) ([]PendingInput, error) {
	messages := b.Drain()
	if limit > 0 && len(messages) > limit {
		b.mu.Lock()
		b.messages = append(messages[limit:], b.messages...)
		b.mu.Unlock()
		messages = messages[:limit]
	}
	inputs := make([]PendingInput, 0, len(messages))
	for _, message := range messages {
		b.mu.Lock()
		b.nextID++
		id := strconv.FormatUint(b.nextID, 10)
		b.mu.Unlock()
		inputs = append(inputs, PendingInput{ID: id, Message: message})
	}
	return inputs, nil
}

func (*InputBuffer) Ack(context.Context, string) error { return nil }

func (b *InputBuffer) Close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
}

type TurnRequest struct {
	SessionID string
	TurnID    string
	Input     model.Message
	// History is an optional, bounded transport import used only when the
	// canonical session has no prior events. It is recorded as ordinary
	// untrusted conversation messages before the current turn.
	History  []model.Message
	Prompt   []prompt.Fragment
	Scope    tool.Scope
	Steering InputSource
	Model    string
}

type TurnResult struct {
	SessionID   string
	TurnID      string
	Message     model.Message
	Usage       model.Usage
	Steps       int
	Termination TerminationReason
}

func (r *Runtime) lockSession(sessionID string) func() {
	r.lockMu.Lock()
	entry := r.locks[sessionID]
	if entry == nil {
		entry = &sessionMutex{}
		r.locks[sessionID] = entry
	}
	entry.refs++
	r.lockMu.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		r.lockMu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(r.locks, sessionID)
		}
		r.lockMu.Unlock()
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
