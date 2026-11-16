package slackhome

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/connections"
	"github.com/noknov/kepler-agent/packages/userprefs"
)

type contextKey struct{}
type contextPreferences struct {
	userprefs.Store
	t     *testing.T
	reads int
}

func (s *contextPreferences) check(ctx context.Context) {
	s.t.Helper()
	s.reads++
	if ctx.Value(contextKey{}) != "request" || ctx.Err() != context.Canceled {
		s.t.Error("preference read escaped canceled request context")
	}
}
func (s *contextPreferences) GetSettings(ctx context.Context, _ string) (userprefs.Settings, error) {
	s.check(ctx)
	return userprefs.Settings{}, ctx.Err()
}
func (s *contextPreferences) ListAssets(ctx context.Context, _ string, _ userprefs.AssetKind) ([]userprefs.Asset, error) {
	s.check(ctx)
	return nil, ctx.Err()
}

type contextConnections struct {
	connections.Store
	t     *testing.T
	reads int
}

func (s *contextConnections) List(ctx context.Context, _ string) ([]connections.Connection, error) {
	s.reads++
	if ctx.Value(contextKey{}) != "request" || ctx.Err() != context.Canceled {
		s.t.Error("connection read escaped canceled request context")
	}
	return nil, ctx.Err()
}
func TestHomeReadsRetainRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "request"))
	cancel()
	prefs := &contextPreferences{t: t}
	conns := &contextConnections{t: t}
	c := Controller{Store: prefs, Connections: connections.Service{Store: conns}}
	c.View(ctx, "U1")
	if prefs.reads != 3 || conns.reads != 1 {
		t.Fatalf("reads settings/assets=%d connections=%d", prefs.reads, conns.reads)
	}
}
