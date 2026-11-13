// Package taskspace owns native Slack channel grants, not agent execution or
// transcript state. Creating a channel never moves an in-flight runtime turn.
package taskspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("Slack task space not found")

type Space struct {
	TeamID          string
	Channel         string
	OwnerID         string
	OriginChannel   string
	OriginMessageTS string
}

type Store interface {
	Get(context.Context, string, string) (Space, error)
	Save(context.Context, Space) error
}

type PGStore struct{ Pool *pgxpool.Pool }

func (s PGStore) Get(ctx context.Context, team, channel string) (Space, error) {
	var space Space
	err := s.Pool.QueryRow(ctx, `SELECT team_id,channel_id,owner_id,origin_channel_id,origin_message_ts FROM slack_task_spaces WHERE team_id=$1 AND channel_id=$2`, team, channel).Scan(&space.TeamID, &space.Channel, &space.OwnerID, &space.OriginChannel, &space.OriginMessageTS)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return space, err
}

func (s PGStore) Save(ctx context.Context, space Space) error {
	if space.TeamID == "" || space.Channel == "" || space.OwnerID == "" || space.OriginChannel == "" || space.OriginMessageTS == "" {
		return fmt.Errorf("complete Slack task space identity is required")
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO slack_task_spaces(team_id,channel_id,owner_id,origin_channel_id,origin_message_ts) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, space.TeamID, space.Channel, space.OwnerID, space.OriginChannel, space.OriginMessageTS)
	if err != nil {
		return err
	}
	existing, err := s.Get(ctx, space.TeamID, space.Channel)
	if err != nil {
		return err
	}
	if existing != space {
		return fmt.Errorf("Slack task space identity conflict")
	}
	return nil
}

// CheckSchema reports the operator migration prerequisite before accepting work.
func (s PGStore) CheckSchema(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `SELECT team_id,channel_id,owner_id,origin_channel_id,origin_message_ts FROM slack_task_spaces LIMIT 0`)
	if err != nil {
		return fmt.Errorf("native Slack task spaces require deployment migration 021: %w", err)
	}
	return nil
}
