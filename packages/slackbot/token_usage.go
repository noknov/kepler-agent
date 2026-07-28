package slackbot

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/config"
)

type tokenUsageProvider interface {
	Summary(ctx context.Context, now time.Time) (tokenUsageSummary, error)
}

type opencodeTokenUsageClient struct {
	workspaceID string
	authCookie  string
	httpClient  *http.Client

	mu        sync.Mutex
	cached    tokenUsageSummary
	cachedAt  time.Time
	cacheTTL  time.Duration
	lastError string
}

type tokenUsageWindow struct {
	UsagePercent     float64
	PercentRemaining float64
	ResetInSec       int64
}

type tokenUsageSummary struct {
	Rolling *tokenUsageWindow
	Weekly  *tokenUsageWindow
	Monthly *tokenUsageWindow
}

func newTokenUsageProvider(provider string, cfg config.TokenUsageConfig) tokenUsageProvider {
	switch provider {
	case "opencode-go":
		return newOpenCodeGoTokenUsageClient(cfg.OpenCodeGo)
	default:
		return nil
	}
}

func newOpenCodeGoTokenUsageClient(cfg config.OpenCodeGoTokenUsageConfig) *opencodeTokenUsageClient {
	if strings.TrimSpace(cfg.WorkspaceID) == "" || strings.TrimSpace(cfg.AuthCookie) == "" {
		return nil
	}
	return &opencodeTokenUsageClient{
		workspaceID: strings.TrimSpace(cfg.WorkspaceID),
		authCookie:  normalizeTokenUsageAuthCookie(cfg.AuthCookie),
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		cacheTTL:    time.Minute,
	}
}

func normalizeTokenUsageAuthCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "auth=") {
		return raw
	}
	return "auth=" + raw
}

func (c *opencodeTokenUsageClient) Summary(ctx context.Context, now time.Time) (tokenUsageSummary, error) {
	c.mu.Lock()
	if !c.cachedAt.IsZero() && time.Since(c.cachedAt) < c.cacheTTL {
		summary := c.cached
		c.mu.Unlock()
		return summary, nil
	}
	c.mu.Unlock()

	summary, err := c.fetch(ctx, now)

	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.lastError = err.Error()
		return tokenUsageSummary{}, err
	}
	c.cached = summary
	c.cachedAt = time.Now()
	c.lastError = ""
	return summary, nil
}

func (c *opencodeTokenUsageClient) fetch(ctx context.Context, _ time.Time) (tokenUsageSummary, error) {
	url := "https://opencode.ai/workspace/" + c.workspaceID + "/go"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return tokenUsageSummary{}, err
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; slack-copilot-agent/1.0)")
	req.Header.Set("Cookie", c.authCookie)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return tokenUsageSummary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenUsageSummary{}, fmt.Errorf("opencode go page returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return tokenUsageSummary{}, err
	}
	return parseOpenCodeGoUsageHTML(string(data))
}

var scrapedNumberPattern = `(-?\d+(?:\.\d+)?)`

var (
	reRollingPctFirst   = regexp.MustCompile(`rollingUsage:\$R\[\d+\]=\{[^}]*usagePercent:` + scrapedNumberPattern + `[^}]*resetInSec:` + scrapedNumberPattern + `[^}]*\}`)
	reRollingResetFirst = regexp.MustCompile(`rollingUsage:\$R\[\d+\]=\{[^}]*resetInSec:` + scrapedNumberPattern + `[^}]*usagePercent:` + scrapedNumberPattern + `[^}]*\}`)
	reWeeklyPctFirst    = regexp.MustCompile(`weeklyUsage:\$R\[\d+\]=\{[^}]*usagePercent:` + scrapedNumberPattern + `[^}]*resetInSec:` + scrapedNumberPattern + `[^}]*\}`)
	reWeeklyResetFirst  = regexp.MustCompile(`weeklyUsage:\$R\[\d+\]=\{[^}]*resetInSec:` + scrapedNumberPattern + `[^}]*usagePercent:` + scrapedNumberPattern + `[^}]*\}`)
	reMonthlyPctFirst   = regexp.MustCompile(`monthlyUsage:\$R\[\d+\]=\{[^}]*usagePercent:` + scrapedNumberPattern + `[^}]*resetInSec:` + scrapedNumberPattern + `[^}]*\}`)
	reMonthlyResetFirst = regexp.MustCompile(`monthlyUsage:\$R\[\d+\]=\{[^}]*resetInSec:` + scrapedNumberPattern + `[^}]*usagePercent:` + scrapedNumberPattern + `[^}]*\}`)
)

func parseOpenCodeGoUsageHTML(html string) (tokenUsageSummary, error) {
	rolling := parseTokenUsageWindow(html, reRollingPctFirst, reRollingResetFirst)
	weekly := parseTokenUsageWindow(html, reWeeklyPctFirst, reWeeklyResetFirst)
	monthly := parseTokenUsageWindow(html, reMonthlyPctFirst, reMonthlyResetFirst)

	if rolling == nil && weekly == nil && monthly == nil {
		dataSlot := parseTokenUsageDataSlots(html)
		rolling = dataSlot["rolling"]
		weekly = dataSlot["weekly"]
		monthly = dataSlot["monthly"]
	}
	if rolling == nil && weekly == nil && monthly == nil {
		return tokenUsageSummary{}, fmt.Errorf("no opencode usage percentages found")
	}

	return tokenUsageSummary{
		Rolling: rolling,
		Weekly:  weekly,
		Monthly: monthly,
	}, nil
}

func parseTokenUsageWindow(html string, pctFirst, resetFirst *regexp.Regexp) *tokenUsageWindow {
	if match := pctFirst.FindStringSubmatch(html); len(match) == 3 {
		return normalizeTokenUsageWindow(match[1], match[2])
	}
	if match := resetFirst.FindStringSubmatch(html); len(match) == 3 {
		return normalizeTokenUsageWindow(match[2], match[1])
	}
	return nil
}

func normalizeTokenUsageWindow(usagePercentRaw, resetInSecRaw string) *tokenUsageWindow {
	usagePercent, err := strconv.ParseFloat(usagePercentRaw, 64)
	if err != nil || !isFiniteFloat(usagePercent) {
		return nil
	}
	resetInSec, err := strconv.ParseFloat(resetInSecRaw, 64)
	if err != nil || !isFiniteFloat(resetInSec) || resetInSec < 0 {
		return nil
	}
	usagePercent = math.Max(0, usagePercent)
	percentRemaining := math.Max(0, 100-usagePercent)
	return &tokenUsageWindow{
		UsagePercent:     usagePercent,
		PercentRemaining: percentRemaining,
		ResetInSec:       int64(math.Round(resetInSec)),
	}
}

func parseTokenUsageDataSlots(html string) map[string]*tokenUsageWindow {
	result := map[string]*tokenUsageWindow{}
	parts := strings.Split(html, `data-slot="usage-item"`)
	for i := 1; i < len(parts); i++ {
		content := parts[i]

		labelMatch := regexp.MustCompile(`data-slot="usage-label">([^<]+)<`).FindStringSubmatch(content)
		if len(labelMatch) != 2 {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(labelMatch[1]))

		usageMatch := regexp.MustCompile(`data-slot="usage-value">[^0-9]*(\d+(?:\.\d+)?)`).FindStringSubmatch(content)
		if len(usageMatch) != 2 {
			continue
		}

		resetMatch := regexp.MustCompile(`data-slot="(reset-time|reset-now)">([\s\S]*?)</span>`).FindStringSubmatch(content)
		if len(resetMatch) != 3 {
			continue
		}
		resetContent := strings.TrimSpace(regexp.MustCompile(`(?i)Resets?\s*in\s*`).ReplaceAllString(resetMatch[2], ""))
		var resetInSec int64
		if resetMatch[1] == "reset-now" {
			resetInSec = 0
		} else {
			parsed, ok := parseHumanReadableDuration(resetContent)
			if !ok {
				continue
			}
			resetInSec = parsed
		}

		window := normalizeTokenUsageWindow(usageMatch[1], strconv.FormatInt(resetInSec, 10))
		if window == nil {
			continue
		}

		switch {
		case strings.Contains(label, "rolling"):
			result["rolling"] = window
		case strings.Contains(label, "weekly"):
			result["weekly"] = window
		case strings.Contains(label, "monthly"):
			result["monthly"] = window
		}
	}
	return result
}

func parseHumanReadableDuration(raw string) (int64, bool) {
	normalized := strings.ToLower(strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(raw, " ")))
	switch normalized {
	case "reset-now", "reset now", "now", "resets now":
		return 0, true
	}

	var total int64
	var found bool
	for _, item := range []struct {
		re   *regexp.Regexp
		unit int64
	}{
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*days?`), 86400},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*hours?`), 3600},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*minutes?`), 60},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*seconds?`), 1},
	} {
		match := item.re.FindStringSubmatch(normalized)
		if len(match) != 2 {
			continue
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil || !isFiniteFloat(value) {
			continue
		}
		total += int64(math.Round(value * float64(item.unit)))
		found = true
	}
	return total, found
}

func isFiniteFloat(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
