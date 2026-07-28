package conversation

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/noknov/slack-copilot-agent/packages/agent"
	"github.com/noknov/slack-copilot-agent/packages/llm"
)

const maxWebEvidenceItems = 5

type webEvidenceItem struct {
	Title string
	URL   string
}

func webEvidenceMarkdown(messages []llm.Message, locale string) string {
	items := collectWebEvidence(messages, maxWebEvidenceItems)
	if len(items) == 0 {
		return ""
	}

	title := "Web Evidence"
	if locale == agent.LocaleZH {
		title = "网页证据"
	}
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(title)
	b.WriteString(":\n")
	for _, item := range items {
		label := strings.TrimSpace(item.Title)
		if label == "" {
			label = item.URL
		}
		b.WriteString("- ")
		b.WriteString(label)
		if item.URL != "" && item.URL != label {
			b.WriteString(" - ")
			b.WriteString(item.URL)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func collectWebEvidence(messages []llm.Message, limit int) []webEvidenceItem {
	if limit <= 0 {
		limit = maxWebEvidenceItems
	}
	var out []webEvidenceItem
	seen := map[string]bool{}
	add := func(title, rawURL string) {
		if len(out) >= limit {
			return
		}
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" || seen[rawURL] || !looksLikeHTTPURL(rawURL) {
			return
		}
		seen[rawURL] = true
		out = append(out, webEvidenceItem{
			Title: cleanEvidenceTitle(title),
			URL:   rawURL,
		})
	}

	for _, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		switch msg.Name {
		case "web-search":
			for _, item := range parseWebSearchEvidence(msg.Content) {
				add(item.Title, item.URL)
			}
		case "web-read_page":
			item := parseWebPageEvidence(msg.Content)
			add(item.Title, item.URL)
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

var numberedEvidenceTitleRe = regexp.MustCompile(`^\d+\.\s+(.+)$`)

func parseWebSearchEvidence(content string) []webEvidenceItem {
	lines := strings.Split(content, "\n")
	var out []webEvidenceItem
	currentTitle := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if match := numberedEvidenceTitleRe.FindStringSubmatch(line); len(match) == 2 {
			currentTitle = match[1]
			continue
		}
		if rawURL, ok := strings.CutPrefix(line, "url:"); ok {
			out = append(out, webEvidenceItem{Title: currentTitle, URL: strings.TrimSpace(rawURL)})
			currentTitle = ""
			continue
		}
		if rawURL, ok := strings.CutPrefix(line, "URL:"); ok {
			out = append(out, webEvidenceItem{Title: currentTitle, URL: strings.TrimSpace(rawURL)})
			currentTitle = ""
		}
	}
	return out
}

func parseWebPageEvidence(content string) webEvidenceItem {
	var item webEvidenceItem
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if rawURL, ok := strings.CutPrefix(line, "url:"); ok {
			item.URL = strings.TrimSpace(rawURL)
			continue
		}
		if title, ok := strings.CutPrefix(line, "title:"); ok {
			item.Title = strings.TrimSpace(title)
		}
	}
	return item
}

func looksLikeHTTPURL(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
}

func cleanEvidenceTitle(title string) string {
	title = strings.Join(strings.Fields(strings.TrimSpace(title)), " ")
	if title == "" {
		return ""
	}
	if len([]rune(title)) <= 120 {
		return title
	}
	runes := []rune(title)
	return fmt.Sprintf("%s...", string(runes[:117]))
}
