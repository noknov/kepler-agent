package hosted

import (
	"context"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

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
