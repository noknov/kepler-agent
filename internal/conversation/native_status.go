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

	mu                 sync.Mutex
	lastLoadingMessage string
	lastSentAt         time.Time
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
	msg := n.lastLoadingMessage
	n.mu.Unlock()
	n.publish(msg)
}

func (n *nativeThreadStatus) updateLoadingMessage(title string) {
	if n == nil || n.messenger == nil {
		return
	}
	if msg := assistantLoadingMessageText(title); msg != "" {
		n.publish(msg)
	}
}

// publish rate-limits repeated identical messages; new content sends immediately.
func (n *nativeThreadStatus) publish(message string) {
	n.mu.Lock()
	if message == n.lastLoadingMessage && time.Since(n.lastSentAt) < 2*time.Second {
		n.mu.Unlock()
		return
	}
	if message != "" {
		n.lastLoadingMessage = message
	}
	n.lastSentAt = time.Now()
	n.mu.Unlock()

	if message != "" {
		go n.send([]string{message})
		return
	}
	go n.send(nil)
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
			stale := loadingMessage != "" && time.Since(n.lastSentAt) >= 90*time.Second
			n.mu.Unlock()
			if stale {
				n.publish(loadingMessage)
			}
		}
	}
}

func (n *nativeThreadStatus) send(loadingMessages []string) {
	staticStatus := "is thinking"
	if n.locale == agent.LocaleZH {
		staticStatus = "正在思考"
	}
	ctx, cancel := context.WithTimeout(n.ctx, 3*time.Second)
	defer cancel()
	if err := n.messenger.SetThreadStatus(ctx, n.channel, n.threadTS, staticStatus, loadingMessages); err != nil && n.onError != nil {
		n.onError(err)
	}
}

func assistantLoadingMessageText(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return stripContextPrefix(title)
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
