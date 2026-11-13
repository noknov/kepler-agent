package hosted

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

// LoadLatestTerminalTurn reads the two state facts in one database snapshot.
// Tool evidence and model payloads never cross the database boundary for this
// projection; full transcript reads remain available for replay and auditing.
func (s PGTranscript) LoadLatestTerminalTurn(ctx context.Context, sessionID string) ([]transcript.Event, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("agent transcript store is unavailable")
	}
	var terminal, started []byte
	err := s.Pool.QueryRow(ctx, `WITH terminal AS (
 SELECT turn_id,payload FROM agent_transcript_events
 WHERE session_id=$1 AND type=ANY($2::text[])
 ORDER BY sequence DESC LIMIT 1
)
SELECT terminal.payload,started.payload FROM terminal
LEFT JOIN LATERAL (
 SELECT payload FROM agent_transcript_events
 WHERE session_id=$1 AND turn_id=terminal.turn_id AND type=$3
 ORDER BY sequence DESC LIMIT 1
) started ON true`, sessionID, []string{string(transcript.TurnCompleted), string(transcript.TurnFailed), string(transcript.TurnCanceled)}, string(transcript.TurnStarted)).Scan(&terminal, &started)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var events []transcript.Event
	for _, payload := range [][]byte{started, terminal} {
		if payload == nil {
			continue
		}
		event, err := transcript.DecodeEvent(payload)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if len(events) == 2 && events[0].Sequence > events[1].Sequence {
		events[0], events[1] = events[1], events[0]
	}
	return events, nil
}

func (s PGTranscript) LoadLatestTurn(ctx context.Context, sessionID string) ([]transcript.Event, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("agent transcript store is unavailable")
	}
	rows, err := s.Pool.Query(ctx, `WITH started AS (
 SELECT sequence,turn_id,payload FROM agent_transcript_events
 WHERE session_id=$1 AND type='turn_started' ORDER BY sequence DESC LIMIT 1
 ), terminal AS (
 SELECT e.sequence,e.payload FROM agent_transcript_events e,started
 WHERE e.session_id=$1 AND e.turn_id=started.turn_id AND e.sequence>started.sequence
 AND e.type=ANY($2::text[]) ORDER BY e.sequence DESC LIMIT 1
 ) SELECT sequence,payload FROM started UNION ALL SELECT sequence,payload FROM terminal ORDER BY sequence`, sessionID, []string{string(transcript.TurnCompleted), string(transcript.TurnFailed), string(transcript.TurnCanceled)})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []transcript.Event
	for rows.Next() {
		var sequence uint64
		var raw []byte
		if err := rows.Scan(&sequence, &raw); err != nil {
			return nil, err
		}
		event, err := transcript.DecodeEvent(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}
