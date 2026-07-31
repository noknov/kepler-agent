package audit

import (
	"context"
	"testing"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/runs"
	"github.com/noknov/slack-copilot-agent/packages/slack"
)

func TestSendWindowFiltersAllowedUsers(t *testing.T) {
	store := fakeRunStore{summaries: []runs.UserAuditSummary{
		{UserID: "U_ALLOWED", Requests: 2, Conversations: 1},
		{UserID: "U_OTHER", Requests: 9, Conversations: 3},
	}}
	mailer := &fakeMailer{}
	s := Scheduler{
		AllowedUsers: []string{"U_ALLOWED"},
		Runs:         store,
		Slack:        fakeSlackUsers{},
		Mailer:       mailer,
	}

	now := time.Date(2026, 7, 31, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if err := s.SendWindow(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if got, want := len(mailer.to), 1; got != want {
		t.Fatalf("sent messages = %d, want %d", got, want)
	}
	if got, want := mailer.to[0], "U_ALLOWED@example.com"; got != want {
		t.Fatalf("recipient = %q, want %q", got, want)
	}
}

type fakeRunStore struct {
	runs.Store
	summaries []runs.UserAuditSummary
}

func (s fakeRunStore) UserAuditSummaries(context.Context, time.Time, time.Time) ([]runs.UserAuditSummary, error) {
	return s.summaries, nil
}

type fakeSlackUsers struct{}

func (fakeSlackUsers) UserInfo(_ context.Context, userID string) (slack.User, error) {
	return slack.User{Profile: slack.UserProfile{Email: userID + "@example.com"}}, nil
}

type fakeMailer struct {
	to []string
}

func (m *fakeMailer) Send(_ context.Context, to, _, _ string) error {
	m.to = append(m.to, to)
	return nil
}
