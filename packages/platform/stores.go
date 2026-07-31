package platform

import (
	"context"
	"fmt"

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

func NewStores(ctx context.Context, cfg config.StorageConfig) (*Stores, error) {
	pgPool, err := newPGPool(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, err
	}

	stores := &Stores{PGPool: pgPool}
	defer func() {
		if stores != nil {
			stores.Close()
		}
	}()

	rdb, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	stores.Redis = rdb

	sessionStore, err := session.NewPGStoreWithPool(ctx, pgPool)
	if err != nil {
		return nil, err
	}
	stores.Sessions = sessionStore

	runStore, err := runs.NewPGStoreWithPool(ctx, pgPool)
	if err != nil {
		return nil, err
	}
	stores.Runs = runStore

	reminderStore, err := reminder.NewPGStoreWithPool(ctx, pgPool)
	if err != nil {
		return nil, err
	}
	stores.Reminders = reminderStore

	eventInbox, err := eventinbox.NewPGStoreWithPool(ctx, pgPool)
	if err != nil {
		return nil, err
	}
	stores.Events = eventInbox

	userPrefsStore, err := userprefs.NewPGStoreWithPool(ctx, pgPool)
	if err != nil {
		return nil, err
	}
	stores.UserPrefs = userPrefsStore

	result := stores
	stores = nil
	return result, nil
}

func NewEventIngressStores(ctx context.Context, cfg config.StorageConfig) (*EventIngressStores, error) {
	pgPool, err := newPGPool(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, err
	}
	stores := &EventIngressStores{PGPool: pgPool}
	defer func() {
		if stores != nil {
			stores.Close()
		}
	}()

	rdb, err := redisclient.New(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("redis: %w", err)
	}
	stores.Redis = rdb

	eventInbox, err := eventinbox.NewPGStoreWithPool(ctx, pgPool)
	if err != nil {
		return nil, err
	}
	stores.Events = eventInbox

	userPrefsStore, err := userprefs.NewPGStoreWithPool(ctx, pgPool)
	if err != nil {
		return nil, err
	}
	stores.UserPrefs = userPrefsStore

	result := stores
	stores = nil
	return result, nil
}

func (s *Stores) Close() {
	if s == nil {
		return
	}
	if s.Events != nil {
		s.Events.Close()
	}
	if s.Runs != nil {
		s.Runs.Close()
	}
	if s.Sessions != nil {
		s.Sessions.Close()
	}
	if s.Reminders != nil {
		s.Reminders.Close()
	}
	if s.UserPrefs != nil {
		s.UserPrefs.Close()
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
	if s.Events != nil {
		s.Events.Close()
	}
	if s.UserPrefs != nil {
		s.UserPrefs.Close()
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
	return pgPool, nil
}
