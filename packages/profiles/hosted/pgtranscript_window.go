package hosted

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
)

func (s PGTranscript) LoadTurn(ctx context.Context, sessionID, turnID string) ([]transcript.Event, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("agent transcript store is unavailable")
	}
	// numeric avoids overflow on corrupt JSON; only validated backward coverage
	// can exclude old rows. Preserve the requested turn for accounting and replay.
	rows, err := s.Pool.Query(ctx, `WITH checkpoints AS (
 SELECT sequence,CASE WHEN payload->'metadata'->>'covers_through' ~ '^[0-9]+$'
 THEN (payload->'metadata'->>'covers_through')::numeric ELSE 0 END AS coverage
 FROM agent_transcript_events WHERE session_id=$1 AND type='compaction_created'
 AND jsonb_typeof(payload->'message')='object'
 ), checkpoint AS (
 SELECT coverage FROM checkpoints WHERE coverage>0 AND coverage<sequence ORDER BY sequence DESC LIMIT 1
 ), task_state AS (
 SELECT sequence FROM agent_transcript_events WHERE session_id=$1 AND type='task_state_updated' ORDER BY sequence DESC LIMIT 1
 ), current_input AS (
 SELECT sequence FROM agent_transcript_events WHERE session_id=$1 AND type IN ('user_input','steering_input') ORDER BY sequence DESC LIMIT 1
 ) SELECT payload FROM agent_transcript_events WHERE session_id=$1 AND (
 sequence>COALESCE((SELECT coverage FROM checkpoint),0) OR turn_id=$2 OR sequence=(SELECT sequence FROM current_input) OR sequence=(SELECT sequence FROM task_state)) ORDER BY sequence`, sessionID, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []transcript.Event
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event transcript.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
