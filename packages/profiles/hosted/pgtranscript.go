package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/agent/transcript"
)

// PGTranscript stores canonical events as the durable conversation source of
// truth. Run records and UI state are projections of this event stream.
type PGTranscript struct{ Pool *pgxpool.Pool }

func (s PGTranscript) Append(ctx context.Context, event transcript.Event) (transcript.Event, error) {
	if s.Pool == nil {
		return transcript.Event{}, fmt.Errorf("agent transcript store is unavailable")
	}
	key := event.SessionID
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return transcript.Event{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// hashtextextended provides a 64-bit key. hashtext is only 32-bit and can
	// serialize unrelated high-volume sessions on a collision.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return transcript.Event{}, err
	}
	if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM agent_transcript_events WHERE session_id=$1`, key).Scan(&event.Sequence); err != nil {
		return transcript.Event{}, err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return transcript.Event{}, err
	}
	payload = bytes.ReplaceAll(payload, []byte(`\u0000`), nil)
	tag, err := tx.Exec(ctx, `INSERT INTO agent_transcript_events(event_id,session_id,turn_id,sequence,type,status,at,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(event_id) DO NOTHING`, event.ID, key, event.TurnID, event.Sequence, string(event.Type), event.Status, event.Timestamp, payload)
	if err != nil {
		return transcript.Event{}, err
	}
	if tag.RowsAffected() == 0 {
		var existingPayload []byte
		if err := tx.QueryRow(ctx, `SELECT payload FROM agent_transcript_events WHERE event_id=$1`, event.ID).Scan(&existingPayload); err != nil {
			return transcript.Event{}, err
		}
		if err := json.Unmarshal(existingPayload, &event); err != nil {
			return transcript.Event{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return transcript.Event{}, err
	}
	return event, nil
}

func (s PGTranscript) Load(ctx context.Context, sessionID string, after uint64) ([]transcript.Event, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("agent transcript store is unavailable")
	}
	rows, err := s.Pool.Query(ctx, `SELECT payload FROM agent_transcript_events WHERE session_id=$1 AND sequence>$2 ORDER BY sequence`, sessionID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []transcript.Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var event transcript.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
