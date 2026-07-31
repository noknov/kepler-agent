package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/infra/redisclient"
	"github.com/noknov/slack-copilot-agent/packages/runs"
	"github.com/noknov/slack-copilot-agent/packages/slack"
)

type SlackUserClient interface {
	UserInfo(context.Context, string) (slack.User, error)
}

type MailSender interface {
	Send(ctx context.Context, to, subject, htmlBody string) error
}

type Scheduler struct {
	Config       config.AuditConfig
	AllowedUsers []string
	Runs         runs.Store
	Slack        SlackUserClient
	Redis        *redisclient.Client
	Mailer       MailSender
	Now          func() time.Time
}

func (s Scheduler) Start(ctx context.Context) {
	if !s.Config.EmailEnabled {
		return
	}
	loc, err := time.LoadLocation(s.Config.Timezone)
	if err != nil {
		log.Printf("audit: invalid timezone %q: %v", s.Config.Timezone, err)
		return
	}
	hour, minute, err := parseClock(s.Config.EmailTime)
	if err != nil {
		log.Printf("audit: invalid AUDIT_EMAIL_TIME %q: %v", s.Config.EmailTime, err)
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.runIfDue(ctx, loc, hour, minute)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s Scheduler) runIfDue(ctx context.Context, loc *time.Location, hour, minute int) {
	now := s.now().In(loc)
	if now.Hour() != hour || now.Minute() != minute {
		return
	}
	date := now.Format("2006-01-02")
	key := "audit:email:" + date
	if s.Redis != nil {
		ok, err := s.Redis.SetNX(ctx, key, time.Now().UTC().Format(time.RFC3339), 36*time.Hour)
		if err != nil {
			log.Printf("audit: set delivery lock: %v", err)
			return
		}
		if !ok {
			return
		}
	}
	if err := s.SendWindow(ctx, now); err != nil {
		log.Printf("audit: send %s: %v", date, err)
	}
}

func (s Scheduler) SendWindow(ctx context.Context, now time.Time) error {
	if s.Runs == nil || s.Slack == nil {
		return fmt.Errorf("runs store and Slack client are required")
	}
	mailer := s.Mailer
	if mailer == nil {
		mailer = CLIMailer{Path: s.Config.CLIPath}
	}
	startLocal, endLocal := previousDayWindow(now)
	summaries, err := s.Runs.UserAuditSummaries(ctx, startLocal, endLocal)
	if err != nil {
		return err
	}
	if len(summaries) == 0 {
		return nil
	}
	summaries = filterAllowedSummaries(summaries, s.AllowedUsers)
	if len(summaries) == 0 {
		return nil
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].TotalTokens > summaries[j].TotalTokens
	})
	limit := s.Config.MaxRecipients
	if limit <= 0 || limit > 50 {
		limit = 45
	}
	if len(summaries) > limit {
		summaries = summaries[:limit]
	}
	for _, summary := range summaries {
		if err := s.sendSummary(ctx, mailer, startLocal, endLocal, summary); err != nil {
			log.Printf("audit: send to user %s: %v", summary.UserID, err)
			continue
		}
	}
	return nil
}

func (s Scheduler) SendUserWindow(ctx context.Context, userID string, now time.Time) (runs.UserAuditSummary, error) {
	if s.Runs == nil || s.Slack == nil {
		return runs.UserAuditSummary{}, fmt.Errorf("runs store and Slack client are required")
	}
	mailer := s.Mailer
	if mailer == nil {
		mailer = CLIMailer{Path: s.Config.CLIPath}
	}
	startLocal, endLocal := previousDayWindow(now)
	summaries, err := s.Runs.UserAuditSummaries(ctx, startLocal, endLocal)
	if err != nil {
		return runs.UserAuditSummary{}, err
	}
	for _, summary := range summaries {
		if summary.UserID != userID || !allowedUser(summary.UserID, s.AllowedUsers) {
			continue
		}
		if err := s.sendSummary(ctx, mailer, startLocal, endLocal, summary); err != nil {
			return runs.UserAuditSummary{}, err
		}
		return summary, nil
	}
	return runs.UserAuditSummary{}, fmt.Errorf("no runs found for user %s in %s", userID, startLocal.Format("2006-01-02"))
}

func filterAllowedSummaries(summaries []runs.UserAuditSummary, allowed []string) []runs.UserAuditSummary {
	if len(summaries) == 0 || len(allowed) == 0 {
		return nil
	}
	out := summaries[:0]
	for _, summary := range summaries {
		if allowedUser(summary.UserID, allowed) {
			out = append(out, summary)
		}
	}
	return out
}

func allowedUser(userID string, allowed []string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	for _, allowedID := range allowed {
		if userID == strings.TrimSpace(allowedID) {
			return true
		}
	}
	return false
}

func (s Scheduler) sendSummary(ctx context.Context, mailer MailSender, startLocal, endLocal time.Time, summary runs.UserAuditSummary) error {
	user, err := s.Slack.UserInfo(ctx, summary.UserID)
	if err != nil {
		return fmt.Errorf("user info: %w", err)
	}
	email := strings.TrimSpace(user.Profile.Email)
	if email == "" {
		return fmt.Errorf("Slack profile has no email")
	}
	subject := fmt.Sprintf("%s usage audit", startLocal.Format("2006-01-02"))
	return mailer.Send(ctx, email, subject, renderSummary(startLocal, endLocal, summary))
}

func (s Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type CLIMailer struct {
	Path string
}

func (m CLIMailer) Send(ctx context.Context, to, subject, htmlBody string) error {
	path := strings.TrimSpace(m.Path)
	if path == "" {
		path = "agently-cli"
	}
	dir, err := os.MkdirTemp("", "usage-audit-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	bodyPath := filepath.Join(dir, "body.html")
	if err := os.WriteFile(bodyPath, []byte(htmlBody), 0o600); err != nil {
		return err
	}
	first, err := runCLI(ctx, path, dir, "message", "+send", "--to", to, "--subject", subject, "--body-file", "body.html")
	if err != nil {
		return err
	}
	token := confirmationToken(first)
	if token == "" {
		return nil
	}
	_, err = runCLI(ctx, path, dir, "message", "+send", "--to", to, "--subject", subject, "--body-file", "body.html", "--confirmation-token", token)
	return err
}

func runCLI(ctx context.Context, path, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", path, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func confirmationToken(out []byte) string {
	var decoded struct {
		Data struct {
			ConfirmationToken string `json:"confirmation_token"`
		} `json:"data"`
	}
	if json.Unmarshal(out, &decoded) == nil {
		return strings.TrimSpace(decoded.Data.ConfirmationToken)
	}
	return ""
}

func previousDayWindow(now time.Time) (time.Time, time.Time) {
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return end.AddDate(0, 0, -1), end
}

func renderSummary(startLocal, endLocal time.Time, s runs.UserAuditSummary) string {
	return fmt.Sprintf(`<p>%s usage audit</p>
<ul>
  <li>Conversations: %d</li>
  <li>Requests: %d</li>
  <li>Completed: %d</li>
  <li>Failed: %d</li>
  <li>Total tokens: %d</li>
  <li>Estimated cost: $%.4f</li>
</ul>`,
		html.EscapeString(startLocal.Format("2006-01-02 15:04 MST")+" - "+endLocal.Format("2006-01-02 15:04 MST")),
		s.Conversations,
		s.Requests,
		s.Completed,
		s.Failed,
		s.TotalTokens,
		s.EstimatedCostUSD)
}

func parseClock(raw string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected HH:MM")
	}
	t, err := time.Parse("15:04", strings.Join(parts, ":"))
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}
