package slackagent

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	agentruntime "github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/connections"
	conversation "github.com/noknov/kepler-agent/packages/surfaces/slack/conversation"
)

const (
	continuationTTL       = 24 * time.Hour
	continuationClaimTTL  = 5 * time.Minute
	continuationBatchSize = 16 // Each admission has a 10-second budget, within the claim lease.
)

// ConnectionContinuation binds one authorization dependency to its waiting turn.
type ConnectionContinuation struct {
	AuthorizationRevision string                   `json:"authorization_revision,omitempty"`
	SessionKind           conversation.SessionKind `json:"session_kind,omitempty"`
	UserID                string                   `json:"user_id"`
	Provider              string                   `json:"provider"`
	InstanceID            string                   `json:"instance_id"`
	SessionID             string                   `json:"session_id"`
	TurnID                string                   `json:"turn_id"`
	TeamID                string                   `json:"team_id"`
	ConversationType      string                   `json:"conversation_type,omitempty"`
	Channel               string                   `json:"channel"`
	ThreadTS              string                   `json:"thread_ts"`
	claimOwner            string
	generation            string
}
type ConnectionContinuationStore interface {
	Save(context.Context, ConnectionContinuation) error
	ClaimReady(context.Context, int) ([]ConnectionContinuation, error)
	Release(context.Context, ConnectionContinuation) error
	Clear(context.Context, ConnectionContinuation) error
}
type RuntimeConnectionContinuationStore struct{ Store ConnectionContinuationStore }

func (s RuntimeConnectionContinuationStore) Save(ctx context.Context, c agentruntime.ConnectionContinuation) error {
	if s.Store == nil {
		return fmt.Errorf("connection continuation store unavailable")
	}
	return s.Store.Save(ctx, ConnectionContinuation{AuthorizationRevision: c.AuthorizationRevision, UserID: c.Scope.UserID, Provider: c.Provider, InstanceID: c.InstanceID, SessionID: c.Scope.SessionID, TurnID: c.TurnID, SessionKind: conversation.SessionKind(c.Scope.Values["session_kind"]), TeamID: c.Scope.Values["team_id"], ConversationType: c.Scope.Values["conversation_type"], Channel: c.Scope.Values["channel"], ThreadTS: c.Scope.Values["thread_ts"]})
}

type PGContinuationStore struct{ Pool *pgxpool.Pool }

func NewPGContinuationStore(ctx context.Context, pool *pgxpool.Pool) (*PGContinuationStore, error) {
	// Check both sides of the durable handoff before accepting any new waiting turns.
	rows, err := pool.Query(ctx, `SELECT p.generation,a.revision FROM slack_connection_continuations p CROSS JOIN connection_authorizations a LIMIT 0`)
	if err != nil {
		return nil, fmt.Errorf("connection continuation schema: %w", err)
	}
	rows.Close()
	return &PGContinuationStore{Pool: pool}, nil
}
func (s PGContinuationStore) Save(ctx context.Context, c ConnectionContinuation) error {
	target := conversation.Target{Kind: c.SessionKind, Channel: c.Channel, ThreadTS: c.ThreadTS}
	if c.UserID == "" || c.Provider == "" || c.TeamID == "" || c.TurnID == "" || target.Validate() != nil || c.SessionID != target.SessionID() {
		return fmt.Errorf("continuation requires authenticated actor/team, provider, target and waiting turn")
	}
	c.InstanceID = connections.NormalizeInstanceID(c.InstanceID)
	if c.InstanceID == "" {
		c.InstanceID = connections.DefaultInstanceID
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO slack_connection_continuations(user_id,provider,instance_id,session_id,turn_id,after_revision,payload,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,NOW()+make_interval(secs=>$8))
 ON CONFLICT(user_id,provider,instance_id,session_id) DO UPDATE SET
 turn_id=EXCLUDED.turn_id,after_revision=EXCLUDED.after_revision,payload=EXCLUDED.payload,
 generation=gen_random_uuid()::text,claim_owner=NULL,claim_until=NULL,attempts=0,
 created_at=NOW(),available_at=NOW(),expires_at=EXCLUDED.expires_at
 WHERE slack_connection_continuations.turn_id<>EXCLUDED.turn_id`, c.UserID, c.Provider, c.InstanceID, c.SessionID, c.TurnID, c.AuthorizationRevision, payload, int64(continuationTTL/time.Second))
	return err
}
func (s PGContinuationStore) ClaimReady(ctx context.Context, limit int) ([]ConnectionContinuation, error) {
	if limit <= 0 || limit > continuationBatchSize {
		limit = continuationBatchSize
	}
	owner := rand.Text()
	rows, err := s.Pool.Query(ctx, `WITH picked AS (
 SELECT p.user_id,p.provider,p.instance_id,p.session_id FROM slack_connection_continuations p
 JOIN connection_authorizations a USING(user_id,provider,instance_id)
 JOIN user_connections c USING(user_id,provider,instance_id)
 WHERE a.revision<>p.after_revision AND c.status='connected' AND p.expires_at>NOW()
 AND p.available_at<=NOW() AND (p.claim_until IS NULL OR p.claim_until<=NOW())
 ORDER BY p.created_at FOR UPDATE OF p SKIP LOCKED LIMIT $1
 ) UPDATE slack_connection_continuations p SET claim_owner=$2,claim_until=NOW()+make_interval(secs=>$3),attempts=attempts+1
 FROM picked x WHERE p.user_id=x.user_id AND p.provider=x.provider AND p.instance_id=x.instance_id AND p.session_id=x.session_id
 RETURNING p.payload,p.generation`, limit, owner, int64(continuationClaimTTL/time.Second))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectionContinuation
	for rows.Next() {
		var raw []byte
		var c ConnectionContinuation
		if err := rows.Scan(&raw, &c.generation); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		c.claimOwner = owner
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s PGContinuationStore) Clear(ctx context.Context, c ConnectionContinuation) error {
	if c.claimOwner == "" || c.generation == "" {
		return fmt.Errorf("continuation cleanup requires its claim")
	}
	_, err := s.Pool.Exec(ctx, `DELETE FROM slack_connection_continuations WHERE user_id=$1 AND provider=$2 AND instance_id=$3 AND session_id=$4 AND generation=$5 AND claim_owner=$6 AND claim_until>NOW()`, c.UserID, c.Provider, c.InstanceID, c.SessionID, c.generation, c.claimOwner)
	return err
}
func (s PGContinuationStore) Release(ctx context.Context, c ConnectionContinuation) error {
	if c.claimOwner == "" || c.generation == "" {
		return fmt.Errorf("continuation release requires its claim")
	}
	_, err := s.Pool.Exec(ctx, `UPDATE slack_connection_continuations SET claim_owner=NULL,claim_until=NULL,
 available_at=NOW()+make_interval(secs=>LEAST(60,POWER(2,LEAST(attempts,6)))::int)
 WHERE user_id=$1 AND provider=$2 AND instance_id=$3 AND session_id=$4 AND generation=$5 AND claim_owner=$6 AND claim_until>NOW()`, c.UserID, c.Provider, c.InstanceID, c.SessionID, c.generation, c.claimOwner)
	return err
}
