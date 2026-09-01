package cloud

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/infra/redisclient"
	"github.com/redis/go-redis/v9"
)

const (
	sessionKeyPrefix = "cli:session:"
	oauthKeyPrefix   = "cli:oauth:"
	deviceKeyPrefix  = "cli:device:"
	SessionTTL       = 30 * 24 * time.Hour
	oauthTTL         = 10 * time.Minute
)

type Session struct {
	UserID    string    `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

type OAuthPending struct {
	DeviceCode string `json:"device_code"`
}

type Device struct {
	Status string `json:"status"`
	State  string `json:"state,omitempty"`
	Token  string `json:"token,omitempty"`
	UserID string `json:"user_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Store struct {
	Redis *redisclient.Client
}

func (s Store) Issue(ctx context.Context, userID string) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(Session{UserID: userID, CreatedAt: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	if err := s.Redis.Set(ctx, sessionKeyPrefix+token, string(payload), SessionTTL); err != nil {
		return "", err
	}
	return token, nil
}

func (s Store) Lookup(ctx context.Context, token string) (Session, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Session{}, errors.New("missing token")
	}
	raw, err := s.Redis.Get(ctx, sessionKeyPrefix+token)
	if errors.Is(err, redis.Nil) {
		return Session{}, errors.New("session expired or unknown")
	}
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal([]byte(raw), &session); err != nil {
		return Session{}, err
	}
	if strings.TrimSpace(session.UserID) == "" {
		return Session{}, errors.New("session is missing a Slack user")
	}
	return session, nil
}

func (s Store) PutOAuth(ctx context.Context, state, deviceCode string) error {
	payload, err := json.Marshal(OAuthPending{DeviceCode: deviceCode})
	if err != nil {
		return err
	}
	return s.Redis.Set(ctx, oauthKeyPrefix+state, string(payload), oauthTTL)
}

func (s Store) TakeOAuth(ctx context.Context, state string) (OAuthPending, error) {
	key := oauthKeyPrefix + state
	raw, err := s.Redis.Get(ctx, key)
	if errors.Is(err, redis.Nil) {
		return OAuthPending{}, errors.New("oauth state expired or unknown")
	}
	if err != nil {
		return OAuthPending{}, err
	}
	_ = s.Redis.Del(ctx, key)
	var pending OAuthPending
	if err := json.Unmarshal([]byte(raw), &pending); err != nil {
		return OAuthPending{}, err
	}
	return pending, nil
}

func (s Store) StartDevice(ctx context.Context, state string) (string, error) {
	device, err := randomToken(24)
	if err != nil {
		return "", err
	}
	if err := s.putDevice(ctx, device, Device{Status: "pending", State: state}); err != nil {
		return "", err
	}
	if err := s.PutOAuth(ctx, state, device); err != nil {
		return "", err
	}
	return device, nil
}

func (s Store) GetDevice(ctx context.Context, device string) (Device, error) {
	raw, err := s.Redis.Get(ctx, deviceKeyPrefix+device)
	if errors.Is(err, redis.Nil) {
		return Device{}, errors.New("login request expired or unknown")
	}
	if err != nil {
		return Device{}, err
	}
	var record Device
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return Device{}, err
	}
	return record, nil
}

func (s Store) CompleteDevice(ctx context.Context, device, token, userID string) error {
	return s.putDevice(ctx, device, Device{Status: "complete", Token: token, UserID: userID})
}

func (s Store) FailDevice(ctx context.Context, device, message string) error {
	return s.putDevice(ctx, device, Device{Status: "error", Error: message})
}

func (s Store) ConsumeDevice(ctx context.Context, device string) (Device, error) {
	record, err := s.GetDevice(ctx, device)
	if err != nil {
		return Device{}, err
	}
	if record.Status == "complete" {
		_ = s.Redis.Del(ctx, deviceKeyPrefix+device)
	}
	return record, nil
}

func (s Store) putDevice(ctx context.Context, device string, record Device) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return s.Redis.Set(ctx, deviceKeyPrefix+device, string(payload), oauthTTL)
}

func randomToken(n int) (string, error) {
	data := make([]byte, n)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(data), nil
}
