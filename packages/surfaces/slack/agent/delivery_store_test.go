package slackagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/sessioninput"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

func TestIntegrationReplyReceiptFencesRestartAndUncertainCreation(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, phase := range []string{"streaming", "starting", "posting"} {
		t.Run(phase, func(t *testing.T) {
			id := fmt.Sprintf("reply-%s-%d", phase, time.Now().UnixNano())
			inputs := sessioninput.NewPGStore(pool)
			store := PGDeliveryStore{Pool: pool}
			defer pool.Exec(context.Background(), `DELETE FROM slack_reply_receipts WHERE turn_id=$1`, id)
			defer pool.Exec(context.Background(), `DELETE FROM agent_session_inputs WHERE id=$1`, id)
			if err := inputs.Enqueue(t.Context(), sessioninput.Item{ID: id, SessionID: id, Kind: sessioninput.KindQueue, Payload: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			claimed, err := inputs.Claim(t.Context(), id, sessioninput.KindQueue, "owner-1", time.Minute, 1)
			if err != nil || len(claimed) != 1 {
				t.Fatalf("claim=%v error=%v", claimed, err)
			}
			receipt, err := store.Begin(t.Context(), ReplyReceipt{TurnID: id, SessionID: id, InputID: id, Owner: "owner-1", Attempt: claimed[0].Attempts, Channel: "C", ThreadTS: "1.0", UserID: "U"})
			if err != nil {
				t.Fatal(err)
			}
			intent := "starting"
			if phase == "posting" {
				intent = "posting"
			}
			if err := store.Advance(t.Context(), receipt, intent, ""); err != nil {
				t.Fatal(err)
			}
			receipt.Phase = intent
			if phase == "streaming" {
				if err := store.Advance(t.Context(), receipt, "streaming", "123.456"); err != nil {
					t.Fatal(err)
				}
				receipt.Phase = "streaming"
				receipt.MessageTS = "123.456"
			}
			// Simulate owner loss by expiring its claim, then let another process claim.
			if _, err := pool.Exec(t.Context(), `UPDATE agent_session_inputs SET claim_until=NOW()-INTERVAL '1 second' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			reclaimed, err := inputs.Claim(t.Context(), id, sessioninput.KindQueue, "owner-2", time.Minute, 1)
			if err != nil || len(reclaimed) != 1 {
				t.Fatalf("reclaim=%v error=%v", reclaimed, err)
			}
			staleTarget := "uncertain"
			if phase == "streaming" {
				staleTarget = "delivered"
			}
			if err := store.Advance(t.Context(), receipt, staleTarget, receipt.MessageTS); !errors.Is(err, sessioninput.ErrClaimLost) {
				t.Fatalf("stale owner advanced delivery: %v", err)
			}
			service := &Service{Deliveries: store, PodID: "owner-2"}
			resumed, err := service.beginDelivery(t.Context(), id, conversation.Request{EventID: id, ClaimID: id, ClaimAttempt: reclaimed[0].Attempts, Channel: "C", ThreadTS: "1.0", UserID: "U"})
			if err != nil {
				t.Fatal(err)
			}
			if phase == "streaming" {
				if resumed.receipt.MessageTS != "123.456" || resumed.receipt.Phase != "streaming" {
					t.Fatalf("lost known reply: %+v", resumed.receipt)
				}
			} else {
				if resumed.receipt.Phase != "uncertain" {
					t.Fatalf("unreceipted create became repeatable: %+v", resumed.receipt)
				}
				if err := resumed.advance(t.Context(), "posting", ""); err == nil {
					t.Fatal("uncertain message creation was repeated")
				}
			}
			altered := resumed.receipt
			altered.UserID = "another-user"
			if _, err := store.Begin(t.Context(), altered); err == nil {
				t.Fatal("reply identity was overwritten")
			}
		})
	}
}
