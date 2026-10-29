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
