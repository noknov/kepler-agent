package slackhome

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/noknov/kepler-agent/packages/safety"
	"github.com/noknov/kepler-agent/packages/surfaces/slack/taskspace"
)

type homeSpaces struct {
	calls int
	err   error
}

func (s *homeSpaces) Get(context.Context, string, string) (taskspace.Space, error) {
	return taskspace.Space{}, taskspace.ErrNotFound
}
func (*homeSpaces) Save(context.Context, taskspace.Space) error { return nil }
func (s *homeSpaces) List(_ context.Context, team, user string, limit int) ([]taskspace.Space, error) {
	s.calls++
	if team != "T1" || user != "U1" || limit > 20 {
		panic("task-space query lost bounded signed identity")
	}
	return []taskspace.Space{
		{TeamID: "T1", OwnerID: "U1", Channel: "COWN", OriginChannel: "CORIGIN"},
		{TeamID: "T1", OwnerID: "U2", Channel: "CFOREIGNUSER", OriginChannel: "CORIGIN"},
		{TeamID: "OTHER", OwnerID: "U1", Channel: "CFOREIGNTEAM", OriginChannel: "CORIGIN"},
		{TeamID: "T1", OwnerID: "U1", Channel: "CREVOKED", OriginChannel: "CREVOKEDORIGIN"},
	}, s.err
}
func TestPersonalHomeTaskSpacesKeepOwnerWorkspaceAndOriginPolicy(t *testing.T) {
	store := &homeSpaces{}
	publisher := &stubPublisher{}
	access := safety.NewAccessPolicy([]string{"U1"}, []string{"CORIGIN"})
	controller := Controller{TeamID: "T1", Access: access, Slack: publisher, Spaces: &taskspace.Service{Store: store, Access: access}}
	if err := controller.Publish(t.Context(), "U1"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(publisher.view)
	body := string(raw)
	if !strings.Contains(body, "COWN") || !strings.Contains(body, "app_redirect?") {
		t.Fatalf("missing task navigation: %s", body)
	}
	for _, hidden := range []string{"CFOREIGNUSER", "CFOREIGNTEAM", "CREVOKED", "instruction", "approval"} {
		if strings.Contains(body, hidden) {
			t.Fatalf("private/unauthorized task data exposed: %s", hidden)
		}
	}
	before := store.calls
	for _, copy := range []Controller{{TeamID: "T1", Access: access, Spaces: controller.Spaces}, {TeamID: "", Access: access, Spaces: controller.Spaces}} {
		copy.View(t.Context(), "not-allowed")
		if store.calls != before {
			t.Fatal("unauthorized home queried task spaces")
		}
	}
	store.err = errors.New("private database diagnostics")
	view := controller.View(t.Context(), "U1")
	raw, _ = json.Marshal(view)
	if !strings.Contains(string(raw), "temporarily unavailable") || strings.Contains(string(raw), "private database diagnostics") {
		t.Fatal("failed navigation was hidden or leaked diagnostics")
	}
}
