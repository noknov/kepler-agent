package conversation

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/wati/oncall-agent/internal/agent"
)

type nativeThreadStatus struct {
	ctx       context.Context
	messenger ThreadStatusMessenger
	channel   string
	threadTS  string
	locale    string
	onError   func(error)

	mu                   sync.Mutex
	lastLoadingMessage   string
	lastLoadingMessageAt time.Time
}

func newNativeThreadStatus(ctx context.Context, messenger ThreadStatusMessenger, channel, threadTS, locale string, onError func(error)) *nativeThreadStatus {
	// Pre-fill with a locale-appropriate default so the very first updateStatic()
	// sends a non-empty loading_messages array. Without this, Slack falls back to
	// its own rotating "Processing…" carousel until the first LLM summary arrives.
	initialLoading := "Thinking..."
	if locale == agent.LocaleZH {
		initialLoading = "思考中..."
	}
	return &nativeThreadStatus{
		ctx:                ctx,
		messenger:          messenger,
		channel:            channel,
		threadTS:           threadTS,
		locale:             locale,
		onError:            onError,
		lastLoadingMessage: initialLoading,
	}
}

func (n *nativeThreadStatus) updateStatic() {
	if n == nil || n.messenger == nil {
		return
	}
	n.mu.Lock()
	loadingMessage := n.lastLoadingMessage
	n.mu.Unlock()
	if loadingMessage != "" {
		go n.send([]string{loadingMessage})
		return
	}
	go n.send(nil)
}

func (n *nativeThreadStatus) updateLoadingMessage(title string) {
	if n == nil || n.messenger == nil {
		return
	}
	loadingMessage := assistantLoadingMessageText(title, n.locale)
	if loadingMessage == "" {
		return
	}
	n.mu.Lock()
	if loadingMessage == n.lastLoadingMessage && time.Since(n.lastLoadingMessageAt) < 5*time.Second {
		n.mu.Unlock()
		return
	}
	if time.Since(n.lastLoadingMessageAt) < 500*time.Millisecond {
		n.mu.Unlock()
		return
	}
	n.lastLoadingMessage = loadingMessage
	n.lastLoadingMessageAt = time.Now()
	n.mu.Unlock()
	go n.send([]string{loadingMessage})
}

func (n *nativeThreadStatus) keepAlive() {
	if n == nil || n.messenger == nil {
		return
	}
	ticker := time.NewTicker(90 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
			n.mu.Lock()
			loadingMessage := n.lastLoadingMessage
			elapsed := time.Since(n.lastLoadingMessageAt)
			if loadingMessage != "" && elapsed >= 90*time.Second {
				n.lastLoadingMessageAt = time.Now()
			}
			n.mu.Unlock()
			if loadingMessage != "" && elapsed >= 90*time.Second {
				n.send([]string{loadingMessage})
			}
		}
	}
}

func (n *nativeThreadStatus) send(loadingMessages []string) {
	staticStatus := "Thinking"
	if n.locale == agent.LocaleZH {
		staticStatus = "思考中"
	}
	ctx, cancel := context.WithTimeout(n.ctx, 3*time.Second)
	defer cancel()
	if err := n.messenger.SetThreadStatus(ctx, n.channel, n.threadTS, staticStatus, loadingMessages); err != nil && n.onError != nil {
		n.onError(err)
	}
}

func assistantLoadingMessageText(title, locale string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	title = stripContextPrefix(title)
	if title == "" {
		return ""
	}
	if locale == agent.LocaleZH {
		if strings.HasPrefix(title, "正在") ||
			strings.HasSuffix(title, "中") ||
			strings.HasSuffix(title, "中...") ||
			strings.HasSuffix(title, "中…") ||
			strings.Contains(title, "等待") ||
			strings.Contains(title, "已") {
			return title
		}
		return "正在" + title
	}
	lower := strings.ToLower(title)
	if strings.HasPrefix(lower, "is ") ||
		strings.HasPrefix(lower, "are ") ||
		strings.HasPrefix(lower, "has ") ||
		strings.HasPrefix(lower, "waiting") {
		return title
	}
	if strings.HasSuffix(lower, "ing") || strings.Contains(lower, "ing ") {
		return "is " + lowerFirstASCII(title)
	}
	return "is " + lowerFirstASCII(title)
}

func stripContextPrefix(title string) string {
	parts := strings.Split(title, " · ")
	if len(parts) == 0 {
		return strings.TrimSpace(title)
	}
	if len(parts) > 1 {
		first := strings.TrimSpace(parts[0])
		last := strings.TrimSpace(parts[len(parts)-1])
		if strings.Contains(strings.ToLower(first), "context") {
			return strings.TrimSpace(strings.Join(parts[1:], " · "))
		}
		if strings.Contains(strings.ToLower(last), "context") {
			return strings.TrimSpace(strings.Join(parts[:len(parts)-1], " · "))
		}
	}
	return strings.TrimSpace(title)
}

func lowerFirstASCII(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'A' && b[0] <= 'Z' {
		b[0] += 'a' - 'A'
	}
	return string(b)
}
