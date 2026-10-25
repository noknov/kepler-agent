package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
)

func TestCanceledTurnIsTerminalForQueue(t *testing.T) {
	store := transcript.NewMemoryStore()
	ctx := context.Background()
	_, err := store.Append(ctx, transcript.Event{ID: "audit_cancel", SessionID: "web_audit", TurnID: "audit_turn", Type: transcript.TurnCanceled, Status: "canceled"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewConversationService(hosted.Agent{}, nil, store, nil)
	terminal := svc.turnTerminal(ctx, "web_audit", "audit_turn")
	t.Logf("TurnCanceled terminal=%v", terminal)
	if !terminal {
		t.Fatal("canceled queue item will be released and retried instead of acknowledged")
	}
}

type failingConversationStore struct {
	Store
	err error
}

func (s failingConversationStore) GetConversation(context.Context, Identity, string) (Conversation, error) {
	return Conversation{}, s.err
}

type claimedWebInput struct {
	capturingInputStore
	item                    sessioninput.Item
	acked, released, claims int
}

func (s *claimedWebInput) Claim(context.Context, string, sessioninput.Kind, string, time.Duration, int) ([]sessioninput.Item, error) {
	s.claims++
	return []sessioninput.Item{s.item}, nil
}
func (s *claimedWebInput) Ack(context.Context, string, string) error     { s.acked++; return nil }
func (s *claimedWebInput) Release(context.Context, string, string) error { s.released++; return nil }

type failingTranscript struct{ transcript.Store }

func (failingTranscript) Append(context.Context, transcript.Event) (transcript.Event, error) {
	return transcript.Event{}, errors.New("storage unavailable")
}

func TestPendingWebReadFailureNeverAcknowledgesInput(t *testing.T) {
	for _, test := range []struct {
		name                 string
		lookup               error
		failAudit            bool
		wantAck, wantRelease int
	}{
		{"temporary lookup failure", errors.New("database unavailable"), false, 0, 1},
		{"permanent not found", ErrNotFound, false, 1, 0},
		{"permanent rejection audit unavailable", ErrNotFound, true, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := Identity{Provider: "slack", TenantID: "T", SubjectID: "U"}
			payload, _ := json.Marshal(queuedWebTurn{Owner: owner, Input: model.TextMessage(model.RoleUser, "keep this request")})
			inputs := &claimedWebInput{item: sessioninput.Item{ID: "turn", Payload: payload, Attempts: 1}}
			var audit transcript.Store = transcript.NewMemoryStore()
			if test.failAudit {
				audit = failingTranscript{audit}
			}
			svc := NewConversationService(hosted.Agent{}, failingConversationStore{err: test.lookup}, audit, nil)
			svc.Inputs = inputs
			svc.startPending(context.Background(), "conversation")
			if inputs.acked != test.wantAck || inputs.released != test.wantRelease {
				t.Fatalf("ack=%d release=%d", inputs.acked, inputs.released)
			}
			events, _ := audit.Load(context.Background(), "conversation", 0)
			if inputs.acked > 0 && len(events) != 1 {
				t.Fatal("acknowledged permanent rejection without durable terminal")
			}
		})
	}
}

func TestPendingWebCapacityDoesNotConsumeRetryAttempts(t *testing.T) {
	inputs := &claimedWebInput{}
	svc := NewConversationService(hosted.Agent{}, nil, transcript.NewMemoryStore(), nil)
	svc.Inputs = inputs
	svc.MaxActiveTurns = 1
	svc.active["busy"] = activeWebTurn{}
	for range 10 {
		svc.startPending(context.Background(), "waiting")
	}
	if inputs.claims != 0 {
		t.Fatalf("full worker consumed %d queue attempts", inputs.claims)
	}
}

func TestIntegrationPendingWebRecoveryPreservesDurableInput(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	session := "web-recovery-" + rand.Text()
	turn := session + "-turn"
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_session_inputs WHERE session_id=$1`, session)
		_, _ = pool.Exec(ctx, `DELETE FROM agent_transcript_events WHERE session_id=$1`, session)
	}()
	owner := Identity{Provider: "slack", TenantID: "T", SubjectID: "U"}
	payload, _ := json.Marshal(queuedWebTurn{Owner: owner, Input: model.TextMessage(model.RoleUser, "do not drop")})
	inputs := sessioninput.NewPGStore(pool)
	if err = inputs.Enqueue(ctx, sessioninput.Item{ID: turn, SessionID: session, Kind: sessioninput.KindWeb, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	audit := hosted.PGTranscript{Pool: pool}
	svc := NewConversationService(hosted.Agent{}, failingConversationStore{err: errors.New("transient database outage")}, audit, nil)
	svc.Inputs = inputs
	svc.startPending(ctx, session)
	var acknowledged bool
	var claim string
	var attempts int
	if err = pool.QueryRow(ctx, `SELECT acknowledged_at IS NOT NULL,claim_owner,attempts FROM agent_session_inputs WHERE id=$1`, turn).Scan(&acknowledged, &claim, &attempts); err != nil {
		t.Fatal(err)
	}
	if acknowledged || claim != "" || attempts != 1 {
		t.Fatalf("lost input: ack=%v claim=%q attempts=%d", acknowledged, claim, attempts)
	}
	// Once storage responds authoritatively, the genuinely missing conversation
	// is a permanent rejection, persisted before acknowledging the queue item.
	svc.Store = PGStore{Pool: pool}
	svc.startPending(ctx, session)
	if err = pool.QueryRow(ctx, `SELECT acknowledged_at IS NOT NULL FROM agent_session_inputs WHERE id=$1`, turn).Scan(&acknowledged); err != nil || !acknowledged {
		t.Fatalf("ack=%v err=%v", acknowledged, err)
	}
	if !svc.turnTerminal(ctx, session, turn) {
		t.Fatal("input ack lacks durable terminal")
	}
}

type unavailableApprovalTranscript struct{ transcript.Store }

func (unavailableApprovalTranscript) Load(context.Context, string, uint64) ([]transcript.Event, error) {
	return nil, errors.New("temporary approval store outage")
}

type unusedWebModel struct{}

func (unusedWebModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	return model.Response{}, errors.New("unexpected generation")
}

func TestPendingWebApprovalStorageFailureRetainsInput(t *testing.T) {
	ctx := context.Background()
	catalog, _ := tool.NewCatalog()
	runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: unusedWebModel{}, Tools: catalog, Transcript: unavailableApprovalTranscript{transcript.NewMemoryStore()}})
	if err != nil {
		t.Fatal(err)
	}
	owner := Identity{Provider: "slack", TenantID: "T", SubjectID: "U"}
	store := newMemoryWebStore()
	if err = store.CreateConversation(ctx, owner, Conversation{ID: "conversation"}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(queuedWebTurn{Owner: owner, Input: model.TextMessage(model.RoleUser, "continue"), Approval: &agentruntime.ApprovalResolution{TurnID: "original", ToolCallID: "write", UserID: "U", Approved: true}})
	inputs := &claimedWebInput{item: sessioninput.Item{ID: "continuation", Payload: payload, Attempts: 1}}
	audit := transcript.NewMemoryStore()
	svc := NewConversationService(hosted.Agent{Runtime: runner}, store, audit, nil)
	svc.Inputs = inputs
	svc.startPending(ctx, "conversation")
	events, _ := audit.Load(ctx, "conversation", 0)
	if inputs.acked != 0 || inputs.released != 1 || len(events) != 0 {
		t.Fatalf("approval incorrectly terminal: ack=%d release=%d events=%d", inputs.acked, inputs.released, len(events))
	}
}

type heldWebAck struct {
	claimedWebInput
	entered, release chan struct{}
}

func (s *heldWebAck) Ack(ctx context.Context, _ string, _ string) error {
	close(s.entered)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func TestWebDrainWaitsForDurableFinalization(t *testing.T) {
	ctx := context.Background()
	owner := Identity{Provider: "slack", TenantID: "T", SubjectID: "U"}
	store := newMemoryWebStore()
	if err := store.CreateConversation(ctx, owner, Conversation{ID: "conversation"}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(queuedWebTurn{Owner: owner, Input: model.TextMessage(model.RoleUser, "request")})
	inputs := &heldWebAck{claimedWebInput: claimedWebInput{item: sessioninput.Item{ID: "turn", Payload: payload, Attempts: 1}}, entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewConversationService(hosted.Agent{}, store, transcript.NewMemoryStore(), nil)
	svc.Inputs = inputs
	svc.BeforeRun = func(context.Context, string) error { return errors.New("startup failure") }
	var draining atomic.Bool
	svc.IsDraining = draining.Load
	svc.startPending(ctx, "conversation")
	<-inputs.entered
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	waiting := svc.WaitContext(canceled)
	// Stop recovery chaining once Ack completes; callbacks run only after this.
	draining.Store(true)
	close(inputs.release)
	svc.Wait()
	if waiting {
		t.Fatal("drain reported idle while durable acknowledgement was still running")
	}
}

func TestWebDrainFencesAdmission(t *testing.T) {
	svc := NewConversationService(hosted.Agent{}, nil, transcript.NewMemoryStore(), nil)
	svc.Drain()
	if svc.launch(Identity{}, "conversation", "turn", model.TextMessage(model.RoleUser, "hi"), nil, "") {
		t.Fatal("draining service launched work")
	}
	if _, err := svc.StartTurn(context.Background(), Identity{}, "conversation", "request_1234567890", "hi"); !errors.Is(err, errServiceUnavailable) {
		t.Fatal(err)
	}
	if !svc.WaitContext(context.Background()) {
		t.Fatal("idle drain failed")
	}
}
