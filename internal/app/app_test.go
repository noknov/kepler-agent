package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/slack"
)

func TestFileShareIsUserMessageSubtype(t *testing.T) {
	if !isUserMessageSubtype("") {
		t.Fatal("empty subtype should be treated as a user message")
	}
	if !isUserMessageSubtype("file_share") {
		t.Fatal("file_share subtype should be treated as a user message")
	}
	if isUserMessageSubtype("bot_message") {
		t.Fatal("bot_message subtype should not be treated as a user message")
	}
}

func TestAppendSlackFiles(t *testing.T) {
	text := appendSlackFiles("please check this", []slack.File{{
		Title:    "error screenshot",
		Mimetype: "image/jpeg",
	}})

	if !strings.Contains(text, "please check this") || !strings.Contains(text, "error screenshot") {
		t.Fatalf("appendSlackFiles() = %q, want original text and file metadata", text)
	}
}

func TestNormalizedImageMIME(t *testing.T) {
	tests := map[string]slack.File{
		"image/png":  {Mimetype: "image/png"},
		"image/jpeg": {Filetype: "jpg"},
		"image/webp": {Filetype: "webp"},
		"":           {Mimetype: "application/pdf", Filetype: "pdf"},
	}
	for want, file := range tests {
		if got := normalizedImageMIME(file); got != want {
			t.Fatalf("normalizedImageMIME(%#v) = %q, want %q", file, got, want)
		}
	}
}

func TestSniffImageMIME(t *testing.T) {
	tests := map[string][]byte{
		"image/png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		"image/jpeg": {0xff, 0xd8, 0xff, 0xe0},
		"image/webp": {'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'E', 'B', 'P'},
		"image/gif":  {'G', 'I', 'F', '8', '9', 'a'},
		"":           []byte("<html><body>not an image</body></html>"),
	}
	for want, data := range tests {
		if got := sniffImageMIME(data); got != want {
			t.Fatalf("sniffImageMIME(%q) = %q, want %q", data, got, want)
		}
	}
}

func TestShouldAttemptSlackTextExcerpt(t *testing.T) {
	tests := []struct {
		name string
		file slack.File
		want bool
	}{
		{name: "unknown extension can still be text", file: slack.File{Name: "incident.payload"}, want: true},
		{name: "markdown", file: slack.File{Name: "runbook.md", Mimetype: "text/markdown"}, want: true},
		{name: "pdf uses pdf extractor", file: slack.File{Name: "invoice.pdf", Mimetype: "application/pdf", Filetype: "pdf"}},
		{name: "image uses image part", file: slack.File{Name: "screenshot.png", Mimetype: "image/png"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldAttemptSlackTextExcerpt(tt.file); got != tt.want {
				t.Fatalf("shouldAttemptSlackTextExcerpt(%#v) = %v, want %v", tt.file, got, tt.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "U123"); got != "U123" {
		t.Fatalf("firstNonEmpty() = %q, want U123", got)
	}
}

func TestIsDMChannel(t *testing.T) {
	if !isDMChannel("D123") {
		t.Fatal("D channel should be treated as DM")
	}
	if isDMChannel("C123") {
		t.Fatal("C channel should not be treated as DM")
	}
}

func TestIsChannelMention(t *testing.T) {
	tests := []struct {
		name string
		ev   slack.Event
		want bool
	}{
		{
			name: "thread app mention",
			ev:   slack.Event{Type: "app_mention", Channel: "C123", TS: "1717000000.000200", ThreadTS: "1717000000.000100"},
			want: true,
		},
		{
			name: "top level app mention",
			ev:   slack.Event{Type: "app_mention", Channel: "C123", TS: "1717000000.000100"},
			want: true,
		},
		{
			name: "plain thread message",
			ev:   slack.Event{Type: "message", Channel: "C123", TS: "1717000000.000200", ThreadTS: "1717000000.000100"},
		},
		{
			name: "dm app mention",
			ev:   slack.Event{Type: "app_mention", Channel: "D123", ChannelType: "im", TS: "1717000000.000200", ThreadTS: "1717000000.000100"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isChannelMention(tt.ev); got != tt.want {
				t.Fatalf("isChannelMention() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsThreadReply(t *testing.T) {
	if !isThreadReply(slack.Event{TS: "1717000000.000200", ThreadTS: "1717000000.000100"}) {
		t.Fatal("message with a different thread_ts should be treated as a thread reply")
	}
	if isThreadReply(slack.Event{TS: "1717000000.000100"}) {
		t.Fatal("top-level message should not be treated as a thread reply")
	}
	if isThreadReply(slack.Event{TS: "1717000000.000100", ThreadTS: "1717000000.000100"}) {
		t.Fatal("root message with matching thread_ts should not be treated as a thread reply")
	}
}

func TestGitFetchEnvDisablesTerminalPrompt(t *testing.T) {
	env := gitFetchEnv()
	if !containsEnv(env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatal("git fetch env should disable terminal prompts")
	}
}

func TestDiscoverWorkspaceReposIncludesRootAndChildren(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, ".git")
	mkdir(t, root, "child", ".git")
	mkdir(t, root, "not-repo")

	repos := discoverWorkspaceRepos([]string{root, root})
	if len(repos) != 2 {
		t.Fatalf("repos = %#v, want root and child only", repos)
	}
	if repos[0] != root {
		t.Fatalf("first repo = %q, want root %q", repos[0], root)
	}
	if repos[1] != root+"/child" {
		t.Fatalf("second repo = %q, want child", repos[1])
	}
}

func TestAuthorizeObservabilityRequiresTokenByDefault(t *testing.T) {
	server := &Server{}
	req, err := http.NewRequest(http.MethodGet, "/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.RemoteAddr = "127.0.0.1:1234"
	if server.authorizeObservability(req) {
		t.Fatal("observability endpoint should require a token by default")
	}
}

func TestAuthorizeObservabilityAcceptsBearerToken(t *testing.T) {
	server := &Server{cfg: config.Config{Observing: config.ObservingConfig{AdminToken: "secret-token"}}}
	req, err := http.NewRequest(http.MethodGet, "/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret-token")
	if !server.authorizeObservability(req) {
		t.Fatal("valid bearer token should authorize observability endpoint")
	}
}

func TestAuthorizeObservabilityAllowsExplicitLocalUnauthenticated(t *testing.T) {
	server := &Server{cfg: config.Config{Observing: config.ObservingConfig{AllowUnauthenticated: true}}}
	req, err := http.NewRequest(http.MethodGet, "/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.RemoteAddr = "[::1]:1234"
	if !server.authorizeObservability(req) {
		t.Fatal("explicit local unauthenticated observability should be allowed")
	}
}

func TestAuthorizeObservabilityRejectsForwardedUnauthenticated(t *testing.T) {
	server := &Server{cfg: config.Config{Observing: config.ObservingConfig{AllowUnauthenticated: true}}}
	req, err := http.NewRequest(http.MethodGet, "/runs", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	if server.authorizeObservability(req) {
		t.Fatal("forwarded request should not be treated as local unauthenticated access")
	}
}

func TestHealthDashboardRequiresObservabilityAccess(t *testing.T) {
	server := &Server{cfg: config.Config{Observing: config.ObservingConfig{AdminToken: "secret-token"}}}
	req := httptest.NewRequest(http.MethodGet, "/health/dashboard", nil)
	rec := httptest.NewRecorder()
	server.handleHealthDashboard(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dashboard without token status = %d, want %d", rec.Code, http.StatusForbidden)
	}

	req = httptest.NewRequest(http.MethodGet, "/health/dashboard", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec = httptest.NewRecorder()
	server.handleHealthDashboard(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard with token status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "斗包 Tool Health") || !strings.Contains(body, "/health/tools?refresh=true") {
		t.Fatalf("dashboard body missing expected content:\n%s", body)
	}
}

func TestHomeViewIncludesModelAndAccess(t *testing.T) {
	server := &Server{cfg: config.Config{LLM: config.LLMConfig{
		Model:           "mimo-v2.5",
		AvailableModels: []string{"mimo-v2.5"},
	}}}
	view := server.homeView("U1")
	text := flattenBlockText(view)
	for _, want := range []string{"斗包", "*Access*", ":heavy_check_mark: Allowed", "*主模型*", "*副模型*", "mimo-v2.5"} {
		if !strings.Contains(text, want) {
			t.Fatalf("home view missing %q in %q", want, text)
		}
	}
	if strings.Contains(text, "Channel-X Copilot Agent") {
		t.Fatalf("home view should not use old product name: %q", text)
	}
}

func TestHomeViewShowsModelWithoutDropdown(t *testing.T) {
	server := &Server{cfg: config.Config{LLM: config.LLMConfig{
		Model:           "mimo-v2.5",
		AvailableModels: []string{"mimo-v2.5"},
	}}}
	view := server.homeView("U1")
	blocks, _ := view["blocks"].([]map[string]any)
	for _, block := range blocks {
		if block["type"] == "actions" {
			t.Fatal("home view should not contain actions block (model dropdown removed)")
		}
	}
	text := flattenBlockText(view)
	if !strings.Contains(text, "*主模型*") || !strings.Contains(text, "*副模型*") || !strings.Contains(text, "mimo-v2.5") {
		t.Fatal("expected model name in home view")
	}
}

func TestHomeViewShowsPrimaryAndSecondaryModels(t *testing.T) {
	server := &Server{cfg: config.Config{LLM: config.LLMConfig{
		Model:          "mimo-v2.5",
		SecondaryModel: "deepseek-v4-flash",
	}}}
	view := server.homeView("U1")
	blocks, _ := view["blocks"].([]map[string]any)
	foundPrimary := false
	foundSecondary := false
	for _, block := range blocks {
		if block["type"] != "section" {
			continue
		}
		for _, field := range blockFields(block) {
			if strings.Contains(field, "主模型") && strings.Contains(field, "mimo-v2.5") {
				foundPrimary = true
			}
			if strings.Contains(field, "副模型") && strings.Contains(field, "deepseek-v4-flash") {
				foundSecondary = true
			}
		}
	}
	if !foundPrimary || !foundSecondary {
		t.Fatalf("expected primary and secondary model fields in home view, primary=%v secondary=%v", foundPrimary, foundSecondary)
	}
}

func blockFields(block map[string]any) []string {
	raw, _ := block["fields"].([]map[string]any)
	out := make([]string, 0, len(raw))
	for _, field := range raw {
		if text, _ := field["text"].(string); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func TestHomeViewDoesNotShowTokenUsage(t *testing.T) {
	server := &Server{cfg: config.Config{LLM: config.LLMConfig{
		Provider:        "opencode-go",
		Model:           "glm-5.2",
		AvailableModels: []string{"glm-5.2", "deepseek-v4-flash"},
	}}}
	server.tokenUsage = &opencodeTokenUsageClient{
		cached: tokenUsageSummary{
			Rolling: &tokenUsageWindow{UsagePercent: 33, PercentRemaining: 67, ResetInSec: 12_300},
		},
		cachedAt: time.Now(),
		cacheTTL: time.Hour,
	}
	view := server.homeView("U1")
	text := flattenBlockText(view)
	if strings.Contains(text, "*Usage*") || strings.Contains(text, "/5h") {
		t.Fatalf("home view should not show usage: %q", text)
	}
}

func TestParseOpenCodeGoUsageHTML(t *testing.T) {
	html := `rollingUsage:$R[1]={usagePercent:33,resetInSec:12300}
weeklyUsage:$R[2]={resetInSec:259200,usagePercent:50}
monthlyUsage:$R[3]={usagePercent:5,resetInSec:2260000}`
	usage, err := parseOpenCodeGoUsageHTML(html)
	if err != nil {
		t.Fatalf("parseOpenCodeGoUsageHTML() error = %v", err)
	}
	if usage.Rolling == nil || usage.Rolling.PercentRemaining != 67 || usage.Rolling.ResetInSec != 12300 {
		t.Fatalf("rolling = %#v, want 67%% left and 12300 reset sec", usage.Rolling)
	}
	if usage.Weekly == nil || usage.Weekly.PercentRemaining != 50 {
		t.Fatalf("weekly = %#v, want 50%% left", usage.Weekly)
	}
	if usage.Monthly == nil || usage.Monthly.PercentRemaining != 95 {
		t.Fatalf("monthly = %#v, want 95%% left", usage.Monthly)
	}
}

func TestTokenUsageSummaryDoesNotReturnStaleCacheOnFetchError(t *testing.T) {
	client := &opencodeTokenUsageClient{
		workspaceID: "wrk_test",
		authCookie:  "auth=test",
		httpClient:  &http.Client{Timeout: time.Second},
		cached: tokenUsageSummary{
			Rolling: &tokenUsageWindow{PercentRemaining: 99},
		},
		cachedAt: time.Now().Add(-time.Hour),
		cacheTTL: time.Minute,
	}
	_, err := client.Summary(context.Background(), time.Now())
	if err == nil {
		t.Fatal("expected fetch error when cache is stale")
	}
}

func mkdir(t *testing.T, parts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(parts...), 0o700); err != nil {
		t.Fatal(err)
	}
}

func containsEnv(env []string, value string) bool {
	for _, item := range env {
		if item == value {
			return true
		}
	}
	return false
}

func flattenBlockText(view map[string]any) string {
	var out strings.Builder
	blocks, _ := view["blocks"].([]map[string]any)
	for _, block := range blocks {
		if text, ok := block["text"].(map[string]any); ok {
			out.WriteString(firstNonEmpty(fmt.Sprint(text["text"]), ""))
			out.WriteString("\n")
		}
		fields, _ := block["fields"].([]map[string]any)
		for _, field := range fields {
			out.WriteString(firstNonEmpty(fmt.Sprint(field["text"]), ""))
			out.WriteString("\n")
		}
	}
	return out.String()
}
