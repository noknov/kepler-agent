package conversation

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type streamMarkdownBuffer struct {
	ctx      context.Context
	channel  string
	append   func(text string) error
	canFlush func() bool

	mu          sync.Mutex
	flushMu     sync.Mutex
	buf         strings.Builder
	lastFlush   time.Time
	err         error
	loopStarted bool
	wake        chan struct{}
	done        chan struct{}
	closed      bool
}

func (b *streamMarkdownBuffer) Write(delta string) {
	if delta == "" {
		return
	}
	b.mu.Lock()
	if b.err != nil || b.closed {
		b.mu.Unlock()
		return
	}
	if !b.loopStarted {
		b.wake = make(chan struct{}, 1)
		b.done = make(chan struct{})
		b.loopStarted = true
		go b.loop()
	}
	b.buf.WriteString(delta)
	if shouldFlushStream(b.channel, b.lastFlush, b.buf.Len()) {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *streamMarkdownBuffer) Close() {
	b.mu.Lock()
	if !b.loopStarted || b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.wake)
	done := b.done
	b.mu.Unlock()
	<-done
}

func (b *streamMarkdownBuffer) Failed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err != nil
}

func (b *streamMarkdownBuffer) loop() {
	b.flush()
	interval, _ := streamFlushConfig(b.channel)
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(b.done)
	for {
		select {
		case <-b.ctx.Done():
			b.flush()
			return
		case _, ok := <-b.wake:
			b.flush()
			if !ok {
				return
			}
		case <-ticker.C:
			b.flush()
		}
	}
}

func (b *streamMarkdownBuffer) flush() {
	b.flushMu.Lock()
	defer b.flushMu.Unlock()

	b.mu.Lock()
	if b.buf.Len() == 0 || (b.canFlush != nil && !b.canFlush()) {
		b.mu.Unlock()
		return
	}
	text := b.buf.String()
	b.buf.Reset()
	b.lastFlush = time.Now()
	appendFn := b.append
	b.mu.Unlock()

	if appendFn == nil {
		return
	}
	if err := appendFn(text); err != nil {
		b.mu.Lock()
		if b.err == nil {
			b.err = err
		}
		b.mu.Unlock()
	}
}

// dmStreamWriter lazily opens a new stream message for the final answer.
type dmStreamWriter struct {
	ctx       context.Context
	messenger Messenger
	channel   string
	threadTS  string
	userID    string
	streamTS  string
	mu        sync.Mutex
	flushMu   sync.Mutex
	buf       strings.Builder
	lastFlush time.Time
	err       error
	started   bool
	wake      chan struct{}
	done      chan struct{}
	closed    bool
}

var (
	streamFlushInterval      = envDuration("STREAM_FLUSH_INTERVAL", 35*time.Millisecond)
	streamFlushChars         = envInt("STREAM_FLUSH_CHARS", 32)
	webStreamFlushInterval   = envDuration("WEB_STREAM_FLUSH_INTERVAL", 16*time.Millisecond)
	webStreamFlushChars      = envInt("WEB_STREAM_FLUSH_CHARS", 16)
	slackStreamFlushInterval = envDuration("SLACK_STREAM_FLUSH_INTERVAL", 50*time.Millisecond)
	slackStreamFlushChars    = envInt("SLACK_STREAM_FLUSH_CHARS", 48)
)

func (w *dmStreamWriter) Write(delta string) {
	if delta == "" {
		return
	}
	w.mu.Lock()
	if w.err != nil || w.closed {
		w.mu.Unlock()
		return
	}
	if !w.started {
		w.wake = make(chan struct{}, 1)
		w.done = make(chan struct{})
		w.started = true
		go w.run()
	}
	w.buf.WriteString(delta)
	if shouldFlushStream(w.channel, w.lastFlush, w.buf.Len()) {
		w.signal()
	}
	w.mu.Unlock()
}

func (w *dmStreamWriter) Flush() {
	w.mu.Lock()
	if !w.started {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	w.flushBuffered()
}

func (w *dmStreamWriter) Close() {
	w.mu.Lock()
	if !w.started || w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.wake)
	done := w.done
	w.mu.Unlock()
	<-done
}

func (w *dmStreamWriter) Failed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err != nil
}

func (w *dmStreamWriter) TS() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.streamTS
}

func (w *dmStreamWriter) run() {
	ts, err := w.messenger.StartStream(w.ctx, w.channel, w.threadTS, w.userID)
	if err != nil {
		log.Printf("answer stream start failed: %v", err)
		w.mu.Lock()
		if w.err == nil {
			w.err = err
		}
		w.mu.Unlock()
		close(w.done)
		return
	}
	w.mu.Lock()
	w.streamTS = ts
	w.mu.Unlock()
	w.flushBuffered()

	interval, _ := streamFlushConfig(w.channel)
	if interval <= 0 {
		interval = 10 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	defer close(w.done)
	for {
		select {
		case <-w.ctx.Done():
			w.flushBuffered()
			return
		case _, ok := <-w.wake:
			w.flushBuffered()
			if !ok {
				return
			}
		case <-ticker.C:
			w.flushBuffered()
		}
	}
}

func (w *dmStreamWriter) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *dmStreamWriter) flushBuffered() {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()

	w.mu.Lock()
	if w.buf.Len() == 0 || w.streamTS == "" {
		w.mu.Unlock()
		return
	}
	text := w.buf.String()
	w.buf.Reset()
	w.lastFlush = time.Now()
	streamTS := w.streamTS
	w.mu.Unlock()

	err := w.messenger.AppendStream(w.ctx, w.channel, streamTS, []map[string]any{
		{"type": "markdown_text", "text": text},
	})
	if err == nil {
		return
	}
	if isSlackStreamExpired(err) {
		newTS, startErr := w.messenger.StartStream(w.ctx, w.channel, w.threadTS, w.userID)
		if startErr == nil && newTS != "" {
			w.mu.Lock()
			w.streamTS = newTS
			w.mu.Unlock()
			retryErr := w.messenger.AppendStream(w.ctx, w.channel, newTS, []map[string]any{
				{"type": "markdown_text", "text": text},
			})
			if retryErr == nil {
				return
			}
			err = retryErr
		} else if startErr != nil {
			err = startErr
		}
	}
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
}

func shouldFlushStream(channel string, lastFlush time.Time, bufLen int) bool {
	interval, chars := streamFlushConfig(channel)
	return interval <= 0 || chars <= 0 || time.Since(lastFlush) > interval || bufLen >= chars
}

func streamFlushConfig(channel string) (time.Duration, int) {
	if strings.HasPrefix(channel, "web:") {
		return webStreamFlushInterval, webStreamFlushChars
	}
	return slackStreamFlushInterval, slackStreamFlushChars
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	if ms, err := strconv.Atoi(raw); err == nil {
		return time.Duration(ms) * time.Millisecond
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return v
}
