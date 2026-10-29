package hosted

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/agent/model"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

type identityModel struct{ calls int }

func (m *identityModel) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	m.calls++
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "done"), FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 1}}, nil
}

func TestIntegrationRuntimeEventsSeparateSessionsWithSharedTurnID(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := PGTranscript{Pool: pool}
	suffix := fmt.Sprint(time.Now().UnixNano())
	for _, prefix := range []string{"owner-a-", "owner-b-"} {
		session := prefix + suffix
		t.Cleanup(func() {
			pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1`, session)
		})
		catalog, _ := tool.NewCatalog()
		client := &identityModel{}
		runner, err := agentruntime.New(agentruntime.Config{}, agentruntime.Dependencies{Model: client, Tools: catalog, Transcript: store})
		if err != nil {
			t.Fatal(err)
		}
		steering := &agentruntime.InputBuffer{}
		steering.Push(model.TextMessage(model.RoleUser, "include the logs"))
		request := agentruntime.TurnRequest{SessionID: session, TurnID: "shared-" + suffix, Input: model.TextMessage(model.RoleUser, "inspect"), Steering: steering}
		for attempt := 0; attempt < 2; attempt++ {
			result, err := runner.RunTurn(t.Context(), request)
			if err != nil || result.Termination != agentruntime.TerminationCompleted || result.Usage.InputTokens != 1 || client.calls != 1 {
				t.Errorf("owner=%s replay=%d result=%+v model_calls=%d err=%v", prefix, attempt, result, client.calls, err)
			}
		}
		events, err := store.Load(t.Context(), session, 0)
		if err != nil {
			t.Fatal(err)
		}
		counts := make(map[transcript.EventType]int)
		for _, event := range events {
			counts[event.Type]++
		}
		for _, kind := range []transcript.EventType{transcript.ModelRequestStarted, transcript.StepStarted, transcript.StepCompleted, transcript.SteeringInput} {
			if counts[kind] != 1 {
				t.Errorf("owner=%s lost or duplicated %s: %d", prefix, kind, counts[kind])
			}
		}
	}
}

func TestIntegrationPGTranscriptDuplicateRequiresSameOwner(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := PGTranscript{Pool: pool}
	session := fmt.Sprintf("duplicate-owner-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM agent_transcript_events WHERE session_id=$1 OR session_id=$2`, session, session+"-other")
	})
	message := model.TextMessage(model.RoleUser, "private original")
	first, err := store.Append(t.Context(), transcript.Event{ID: session, SessionID: session, TurnID: "turn", Type: transcript.UserInput, Message: &message, Timestamp: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct{ session, turn string }{{session + "-other", "turn"}, {session, "another-turn"}} {
		result, err := store.Append(t.Context(), transcript.Event{ID: first.ID, SessionID: owner.session, TurnID: owner.turn})
		if err == nil || result.ID != "" || result.Message != nil {
			t.Errorf("duplicate returned another owner's fact: result=%+v err=%v", result, err)
		}
	}
	again, err := store.Append(t.Context(), transcript.Event{ID: first.ID, SessionID: session, TurnID: "turn"})
	if err != nil || again.ID != first.ID || again.Sequence != first.Sequence || again.Message.Text() != "private original" {
		t.Fatalf("same-owner idempotency lost: result=%+v err=%v", again, err)
	}
}
