package platform

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/eventinbox"
	"github.com/noknov/slack-copilot-agent/packages/infra/envutil"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/reminder"
	"github.com/noknov/slack-copilot-agent/packages/runs"
	"github.com/noknov/slack-copilot-agent/packages/session"
	"github.com/noknov/slack-copilot-agent/packages/userprefs"
)

// Stores owns the durable dependencies shared by app entrypoints.
type Stores struct {
	PGPool    *pgxpool.Pool
	Redis     *redisclient.Client
	Sessions  *session.PGStore
	Runs      *runs.PGStore
	Reminders *reminder.PGStore
	Events    *eventinbox.PGStore
	UserPrefs *userprefs.PGStore
}

type EventIngressStores struct {
	PGPool    *pgxpool.Pool
	Redis     *redisclient.Client
	Events    *eventinbox.PGStore
	UserPrefs *userprefs.PGStore
}

// CoreStores contains only persistence needed by the transport-neutral app
// server. It deliberately excludes Slack inbox, session, and reminder stores.
type CoreStores struct {
	PGPool    *pgxpool.Pool
	Redis     *redisclient.Client
	Runs      *runs.PGStore
	UserPrefs *userprefs.PGStore
}

func NewStores(ctx context.Context, cfg config.StorageConfig) (*Stores, error) {
	pgPool, err := newPGPool(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, err
	}
	if err := requireSchema(ctx, pgPool, allTables); err != nil {
		pgPool.Close()
		return nil, err
	}

	rdb, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		pgPool.Close()
		return nil, fmt.Errorf("redis: %w", err)
	}

	return &Stores{
		PGPool:    pgPool,
		Redis:     rdb,
		Sessions:  session.NewPGStore(pgPool),
		Runs:      runs.NewPGStore(pgPool),
		Reminders: reminder.NewPGStore(pgPool),
		Events:    eventinbox.NewPGStore(pgPool),
		UserPrefs: userprefs.NewPGStore(pgPool),
	}, nil
}

func NewEventIngressStores(ctx context.Context, cfg config.StorageConfig) (*EventIngressStores, error) {
	pgPool, err := newPGPool(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, err
	}
	if err := requireSchema(ctx, pgPool, ingressTables); err != nil {
		pgPool.Close()
		return nil, err
	}
	rdb, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		pgPool.Close()
		return nil, fmt.Errorf("redis: %w", err)
	}

	return &EventIngressStores{
		PGPool:    pgPool,
		Redis:     rdb,
		Events:    eventinbox.NewPGStore(pgPool),
		UserPrefs: userprefs.NewPGStore(pgPool),
	}, nil
}

func NewCoreStores(ctx context.Context, cfg config.StorageConfig) (*CoreStores, error) {
	pgPool, err := newPGPool(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, err
	}
	if err := requireSchema(ctx, pgPool, coreTables); err != nil {
		pgPool.Close()
		return nil, err
	}
	rdb, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		pgPool.Close()
		return nil, fmt.Errorf("redis: %w", err)
	}
	return &CoreStores{
		PGPool:    pgPool,
		Redis:     rdb,
		Runs:      runs.NewPGStore(pgPool),
		UserPrefs: userprefs.NewPGStore(pgPool),
	}, nil
}

func (s *Stores) Close() {
	if s == nil {
		return
	}
	if s.Redis != nil {
		_ = s.Redis.Close()
	}
	if s.PGPool != nil {
		s.PGPool.Close()
	}
}

func (s *EventIngressStores) Close() {
	if s == nil {
		return
	}
	if s.Redis != nil {
		_ = s.Redis.Close()
	}
	if s.PGPool != nil {
		s.PGPool.Close()
	}
}

func (s *CoreStores) Close() {
	if s == nil {
		return
	}
	if s.Redis != nil {
		_ = s.Redis.Close()
	}
	if s.PGPool != nil {
		s.PGPool.Close()
	}
}

func newPGPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pgCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	pgCfg.MaxConns = int32(envutil.Int("POSTGRES_MAX_CONNS", 20))
	pgCfg.MinConns = int32(envutil.Int("POSTGRES_MIN_CONNS", 2))
	pgPool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pgPool.Ping(ctx); err != nil {
		pgPool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return pgPool, nil
}

var allTables = []string{
	"agent_sessions", "agent_runs", "agent_tool_spills", "agent_run_steps",
	"agent_run_feedback", "reminders", "slack_event_inbox", "user_settings",
	"user_prompt_assets",
}

var ingressTables = []string{"slack_event_inbox", "user_settings", "user_prompt_assets"}

var coreTables = []string{
	"agent_runs", "agent_tool_spills", "agent_run_steps", "agent_run_feedback",
	"user_settings", "user_prompt_assets",
}

// requireSchema performs a read-only startup check. Application processes do
// not create or alter database objects and can run without DDL privileges.
func requireSchema(ctx context.Context, pool *pgxpool.Pool, tables []string) error {
	rows, err := pool.Query(ctx, `SELECT name FROM unnest($1::text[]) AS name WHERE to_regclass(name) IS NULL ORDER BY name`, tables)
	if err != nil {
		return fmt.Errorf("verify postgres schema: %w", err)
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("verify postgres schema: %w", err)
		}
		missing = append(missing, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("verify postgres schema: %w", err)
	}
	if len(missing) > 0 {
		return fmt.Errorf("postgres schema is incomplete (missing %s); initialize it with schema/postgres.sql", strings.Join(missing, ", "))
	}
	return nil
}
