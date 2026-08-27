package slackevents

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/eventinbox"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

type fakeInbox struct {
	mu          sync.Mutex
	renews      int
	completed   int
	failed      int
	deadLetters int
	dead        bool
	pending     []eventinbox.Record
	renewErr    error
}

func (f *fakeInbox) Start(context.Context, string, time.Duration) (bool, error) { return true, nil }
func (f *fakeInbox) Renew(context.Context, string, time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews++
	return f.renewErr
}

func TestWorkerCancelsHandlerWhenLeaseIsLost(t *testing.T) {
	inbox := &fakeInbox{renewErr: eventinbox.ErrLeaseLost}
	canceled := make(chan struct{})
	w := &Worker{
		Inbox: inbox, InboxLease: 15 * time.Millisecond, EventTimeout: time.Second,
		Handler: func(ctx context.Context, _ string, _ slack.Event) error {
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		},
	}
	w.handle(context.Background(), job{eventID: "lease-lost"})
	select {
	case <-canceled:
	default:
		t.Fatal("handler was not canceled")
	}
	if inbox.completed != 0 || inbox.failed != 0 {
		t.Fatalf("former owner finalized event: completed=%d failed=%d", inbox.completed, inbox.failed)
	}
}
func (f *fakeInbox) Complete(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed++
	return nil
}
func (f *fakeInbox) Fail(context.Context, string, error, time.Duration, int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed++
	return f.dead, nil
}
func (f *fakeInbox) DeadLetter(context.Context, string, error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadLetters++
	return nil
}
func (f *fakeInbox) RecoverExpired(context.Context, int) error { return nil }
func (f *fakeInbox) Pending(context.Context, int) ([]eventinbox.Record, error) {
	return f.pending, nil
}

func TestWorkerRenewsLeaseAndCompletes(t *testing.T) {
	inbox := &fakeInbox{}
	w := &Worker{
		Inbox:        inbox,
		InboxLease:   15 * time.Millisecond,
		EventTimeout: time.Second,
		Handler: func(context.Context, string, slack.Event) error {
			time.Sleep(25 * time.Millisecond)
			return nil
		},
	}
	w.handle(context.Background(), job{eventID: "E1"})
	if inbox.renews == 0 {
		t.Fatal("expected the worker to renew a long-running event lease")
	}
	if inbox.completed != 1 || inbox.failed != 0 {
		t.Fatalf("completed=%d failed=%d", inbox.completed, inbox.failed)
	}
}

type cancelingRenewInbox struct {
	fakeInbox
	started chan struct{}
}

func (f *cancelingRenewInbox) Renew(ctx context.Context, _ string, _ time.Duration) error {
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestWorkerTreatsCanceledInflightRenewalAsNormalCompletion(t *testing.T) {
	inbox := &cancelingRenewInbox{started: make(chan struct{})}
	w := &Worker{
		Inbox: inbox, InboxLease: 3 * time.Millisecond, EventTimeout: time.Second,
		Handler: func(context.Context, string, slack.Event) error {
			<-inbox.started
			return nil
		},
	}
	w.handle(context.Background(), job{eventID: "renew-canceled-after-handler"})
	if inbox.completed != 1 || inbox.failed != 0 {
		t.Fatalf("completed=%d failed=%d", inbox.completed, inbox.failed)
	}
}

func TestWorkerFailureUsesBoundedRetryPath(t *testing.T) {
	inbox := &fakeInbox{}
	w := &Worker{
		Inbox:        inbox,
		InboxLease:   time.Minute,
		EventTimeout: time.Second,
		MaxAttempts:  3,
		Handler: func(context.Context, string, slack.Event) error {
			return errors.New("downstream unavailable")
		},
	}
	w.handle(context.Background(), job{eventID: "E2", attempt: 1})
	if inbox.failed != 1 || inbox.completed != 0 {
		t.Fatalf("failed=%d completed=%d", inbox.failed, inbox.completed)
	}
}

func TestReplayDeadLettersMalformedPayload(t *testing.T) {
	inbox := &fakeInbox{pending: []eventinbox.Record{{ID: "bad", Payload: []byte("{")}}}
	w := &Worker{Inbox: inbox, queue: make(chan job, 1)}
	w.replay(context.Background())
	if inbox.deadLetters != 1 {
		t.Fatalf("deadLetters=%d, want 1", inbox.deadLetters)
	}
}
