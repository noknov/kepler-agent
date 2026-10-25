package worker

import (
	"context"
	"errors"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/profiles/hosted"
	websurface "github.com/noknov/kepler-agent/packages/surfaces/web"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestDrainIsLocalOnly(t *testing.T) {
	service := &Service{}
	remote := httptest.NewRequest(http.MethodPost, "http://worker/drain", nil)
	remote.RemoteAddr = "203.0.113.10:1234"
	remoteRecorder := httptest.NewRecorder()
	service.handleDrain(remoteRecorder, remote)
	if remoteRecorder.Code != http.StatusForbidden || service.draining.Load() {
		t.Fatalf("remote drain code=%d draining=%v", remoteRecorder.Code, service.draining.Load())
	}

	local := httptest.NewRequest(http.MethodPost, "http://worker/drain", nil)
	local.RemoteAddr = "127.0.0.1:1234"
	localRecorder := httptest.NewRecorder()
	service.handleDrain(localRecorder, local)
	if localRecorder.Code != http.StatusOK || !service.draining.Load() {
		t.Fatalf("local drain code=%d draining=%v", localRecorder.Code, service.draining.Load())
	}
}

type shutdownWebStore struct{ websurface.Store }

func (shutdownWebStore) GetConversation(_ context.Context, _ websurface.Identity, id string) (websurface.Conversation, error) {
	return websurface.Conversation{ID: id}, nil
}
func (shutdownWebStore) TouchConversation(context.Context, websurface.Identity, string, string) error {
	return nil
}

func TestShutdownDeadlineCancelsWithoutUnboundedWebWait(t *testing.T) {
	lifecycle, serviceCancel := context.WithCancel(context.Background())
	defer serviceCancel()
	web := websurface.NewConversationService(hosted.Agent{}, shutdownWebStore{}, transcript.NewMemoryStore(), nil)
	web.Lifecycle = lifecycle
	entered, release := make(chan struct{}), make(chan struct{})
	web.BeforeRun = func(context.Context, string) error {
		close(entered)
		<-release
		return errors.New("blocked adapter stopped")
	}
	_, err := web.StartTurn(context.Background(), websurface.Identity{Provider: "slack", TenantID: "T", SubjectID: "U"}, "conversation", "request_1234567890", "request")
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	service := &Service{cfg: config.Config{HTTP: config.HTTPConfig{ShutdownTimeout: 10 * time.Millisecond}}, cancel: serviceCancel, webConversations: web}
	service.eventCond = sync.NewCond(&service.eventMu)
	signal, stop := context.WithCancel(context.Background())
	stop()
	done := make(chan error, 1)
	go func() { done <- service.RunUntilDone(signal) }()
	defer func() { close(release); web.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown ignored configured grace deadline")
	}
	if lifecycle.Err() == nil {
		t.Fatal("deadline did not cancel service work")
	}
}

func TestEventDrainHonorsContextDeadline(t *testing.T) {
	service := &Service{activeEvents: 1}
	service.eventCond = sync.NewCond(&service.eventMu)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if service.waitEvents(ctx) {
		t.Fatal("reported active Slack event drained")
	}
}

func TestCloseKeepsWebFinalizationInsideCleanupLifetime(t *testing.T) {
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	web := websurface.NewConversationService(hosted.Agent{}, shutdownWebStore{}, transcript.NewMemoryStore(), nil)
	web.Lifecycle = lifecycle
	entered, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	web.BeforeRun = func(ctx context.Context, _ string) error {
		close(entered)
		<-ctx.Done()
		close(cleaning)
		<-release
		return ctx.Err()
	}
	if _, err := web.StartTurn(context.Background(), websurface.Identity{Provider: "slack", TenantID: "T", SubjectID: "U"}, "conversation", "request_1234567890", "request"); err != nil {
		t.Fatal(err)
	}
	<-entered
	service := &Service{cancel: cancel, webConversations: web}
	done := make(chan struct{})
	go func() { service.Close(); close(done) }()
	<-cleaning
	select {
	case <-done:
		close(release)
		web.Wait()
		t.Fatal("closed service before cooperative Web finalization")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("completed Web cleanup did not release Close")
	}
}
