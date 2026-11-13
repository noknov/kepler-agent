package slackagent

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/kepler-agent/packages/sessioninput"
)

// These phases describe network delivery, independently of runtime termination.
const (
	replyPrepared  = "prepared"
	replyStarting  = "starting"
	replyPosting   = "posting"
	replyStreaming = "streaming"
	replyDelivered = "delivered"
	replyUncertain = "uncertain"
)

// ReplyReceipt stores network intent and known Slack identity. It contains no
// answer text: the canonical transcript remains the source of the final answer.
type ReplyReceipt struct {
	TurnID        string
	SessionID     string
	InputID       string
	Owner         string
	Attempt       int
	Channel       string
	ThreadTS      string
	UserID        string
	Phase         string
	MessageTS     string
	DesiredStatus string
	StatusApplied bool
}

type DeliveryStore interface {
	Begin(context.Context, ReplyReceipt) (ReplyReceipt, error)
	Advance(context.Context, ReplyReceipt, string, string) error
	Status(context.Context, ReplyReceipt, string, bool) error
	Pending(context.Context, int) ([]ReplyReceipt, error)
}

type PGDeliveryStore struct{ Pool *pgxpool.Pool }

const receiptColumns = `turn_id,session_id,input_id,owner,attempt,channel,thread_ts,user_id,phase,message_ts,desired_status,status_applied`

type receiptScanner interface{ Scan(...any) error }

func scanReceipt(row receiptScanner) (r ReplyReceipt, err error) {
	err = row.Scan(&r.TurnID, &r.SessionID, &r.InputID, &r.Owner, &r.Attempt, &r.Channel, &r.ThreadTS, &r.UserID, &r.Phase, &r.MessageTS, &r.DesiredStatus, &r.StatusApplied)
	return
}

func (s PGDeliveryStore) Begin(ctx context.Context, r ReplyReceipt) (ReplyReceipt, error) {
	// Only a currently claimed input can start or take over this reply. Existing
	// identity and an equal attempt from another owner can never be overwritten.
	return scanReceipt(s.Pool.QueryRow(ctx, `INSERT INTO slack_reply_receipts
 (turn_id,session_id,input_id,owner,attempt,channel,thread_ts,user_id)
 SELECT $1,$2,$3,$4,$5::bigint,$6,$7,$8 FROM agent_session_inputs
 WHERE id=$3 AND session_id=$2 AND claim_owner=$4 AND attempts::bigint=$5 AND acknowledged_at IS NULL
 ON CONFLICT(turn_id) DO UPDATE SET owner=EXCLUDED.owner,attempt=EXCLUDED.attempt,updated_at=NOW()
 WHERE slack_reply_receipts.session_id=EXCLUDED.session_id AND slack_reply_receipts.input_id=EXCLUDED.input_id
 AND slack_reply_receipts.channel=EXCLUDED.channel AND slack_reply_receipts.thread_ts=EXCLUDED.thread_ts
 AND slack_reply_receipts.user_id=EXCLUDED.user_id AND
 (slack_reply_receipts.attempt<EXCLUDED.attempt OR (slack_reply_receipts.attempt=EXCLUDED.attempt AND slack_reply_receipts.owner=EXCLUDED.owner))
 RETURNING `+receiptColumns, r.TurnID, r.SessionID, r.InputID, r.Owner, r.Attempt, r.Channel, r.ThreadTS, r.UserID))
}

func validReplyTransition(from, to string) bool {
	switch from {
	case replyPrepared:
		return to == replyStarting || to == replyPosting
	case replyStarting:
		return to == replyStreaming || to == replyPrepared || to == replyUncertain
	case replyPosting:
		return to == replyDelivered || to == replyPrepared || to == replyUncertain
	case replyStreaming:
		return to == replyDelivered
	}
	return false
}

func (s PGDeliveryStore) Advance(ctx context.Context, r ReplyReceipt, phase, messageTS string) error {
	if ((phase == replyStreaming || phase == replyDelivered) && messageTS == "") || !validReplyTransition(r.Phase, phase) {
		return fmt.Errorf("invalid Slack delivery transition %s to %s", r.Phase, phase)
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE slack_reply_receipts r SET phase=$6,message_ts=$7,updated_at=NOW()
 WHERE turn_id=$1 AND session_id=$2 AND owner=$3 AND attempt=$4 AND phase=$5
 AND EXISTS(SELECT 1 FROM agent_session_inputs i WHERE i.id=r.input_id AND i.claim_owner=$3 AND i.attempts::bigint=$4 AND i.acknowledged_at IS NULL)`, r.TurnID, r.SessionID, r.Owner, r.Attempt, r.Phase, phase, messageTS)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return sessioninput.ErrClaimLost
	}
	return nil
}

func (s PGDeliveryStore) Status(ctx context.Context, r ReplyReceipt, status string, applied bool) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE slack_reply_receipts SET desired_status=$5,status_applied=$6,updated_at=NOW()
 WHERE turn_id=$1 AND session_id=$2 AND owner=$3 AND attempt=$4
 AND (NOT $6 OR desired_status=$5)`, r.TurnID, r.SessionID, r.Owner, r.Attempt, status, applied)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return sessioninput.ErrClaimLost
	}
	return nil
}

func (s PGDeliveryStore) Pending(ctx context.Context, limit int) ([]ReplyReceipt, error) {
	if limit <= 0 || limit > 100 {
		limit = 32
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+receiptColumns+` FROM slack_reply_receipts WHERE NOT status_applied ORDER BY updated_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ReplyReceipt
	for rows.Next() {
		r, err := scanReceipt(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
