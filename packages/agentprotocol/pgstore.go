package agentprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the durable append-only protocol event log. It assigns a
// monotonically increasing sequence per thread so transports can reconnect and
// replay from their last observed event.
type PGStore struct {
	pool *pgxpool.Pool
}

func NewPGStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

func (s *PGStore) Publish(ctx context.Context, event Event) {
	_, _ = s.Append(ctx, event)
}

func (s *PGStore) Append(ctx context.Context, event Event) (Event, error) {
	if s == nil || s.pool == nil {
		return Event{}, fmt.Errorf("agent protocol store is unavailable")
	}
	event = Normalize(event)
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Event{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, event.ThreadID); err != nil {
		return Event{}, err
	}
	var sequence uint64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM agent_protocol_events WHERE thread_id=$1`, event.ThreadID).Scan(&sequence); err != nil {
		return Event{}, err
	}
	event.Sequence = sequence
	payload, err := marshalEvent(event)
	if err != nil {
		return Event{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_protocol_events(event_id,thread_id,turn_id,sequence,type,status,at,payload)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(event_id) DO NOTHING`, event.ID, event.ThreadID, event.TurnID, event.Sequence, string(event.Type), string(event.Status), event.At, payload)
	if err != nil {
		return Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s *PGStore) Replay(ctx context.Context, threadID string, after uint64, limit int) ([]Event, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("agent protocol store is unavailable")
	}
	if limit <= 0 {
		limit = 512
	}
	if limit > 2048 {
		limit = 2048
	}
	rows, err := s.pool.Query(ctx, `SELECT payload FROM agent_protocol_events WHERE thread_id=$1 AND sequence>$2 ORDER BY sequence LIMIT $3`, threadID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var event Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil && err != pgx.ErrNoRows {
		return nil, err
	}
	return events, nil
}

func marshalEvent(event Event) ([]byte, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	return bytes.ReplaceAll(payload, []byte(`\u0000`), nil), nil
}
