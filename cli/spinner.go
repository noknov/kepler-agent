package cli

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Glyphs match Claude Code's SpinnerGlyph (darwin set, ping-ponged).
// See claude-code/src/components/Spinner/utils.ts getDefaultCharacters.
var spinnerGlyphs = []string{"·", "✢", "✳", "✶", "✻", "✽"}

type waitSpinner struct {
	out     io.Writer
	color   bool
	message string
	stop    chan struct{}
	done    chan struct{}
}

func startWaitSpinner(out io.Writer, color bool) *waitSpinner {
	s := &waitSpinner{
		out:     out,
		color:   color,
		message: randomSpinnerVerb() + "…",
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *waitSpinner) loop() {
	defer close(s.done)
	frames := pingPong(spinnerGlyphs)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	frame := 0
	s.render(frames[0], start)
	for {
		select {
		case <-s.stop:
			fmt.Fprint(s.out, "\r\x1b[2K")
			return
		case now := <-ticker.C:
			frame = int(now.Sub(start).Milliseconds() / 120)
			s.render(frames[frame%len(frames)], now)
		}
	}
}

func (s *waitSpinner) render(glyph string, now time.Time) {
	msg := s.message
	if s.color {
		glyph = paintANSI(true, glyph, colorClaude)
		msg = glimmerText(msg, now, colorClaude, colorClaudeShim)
	}
	fmt.Fprintf(s.out, "\r  %s %s\x1b[0K", glyph, msg)
}

// glimmerText highlights one rune at a time, mirroring GlimmerMessage shimmer.
func glimmerText(text string, now time.Time, base, shimmer string) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return paintANSI(true, text, base)
	}
	cycle := len(runes) + 10
	idx := int(now.UnixMilli()/200) % cycle
	if idx >= len(runes) {
		idx = -1
	}
	var b strings.Builder
	for i, r := range runes {
		ch := string(r)
		switch {
		case i == idx:
			b.WriteString(paintANSI(true, ch, shimmer))
		default:
			b.WriteString(paintANSI(true, ch, base))
		}
	}
	return b.String()
}

func (s *waitSpinner) Stop() {
	if s == nil {
		return
	}
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

func pingPong(in []string) []string {
	out := make([]string, 0, len(in)*2)
	out = append(out, in...)
	for i := len(in) - 1; i >= 0; i-- {
		out = append(out, in[i])
	}
	return out
}

func (r *eventRenderer) startWait() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startWaitLocked()
}

func (r *eventRenderer) startWaitLocked() {
	if r.mode == "jsonl" || r.waiting != nil {
		return
	}
	r.waiting = startWaitSpinner(r.stderr, r.color)
}

func (r *eventRenderer) stopWait() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopWaitLocked()
}

func (r *eventRenderer) stopWaitLocked() {
	if r.waiting == nil {
		return
	}
	r.waiting.Stop()
	r.waiting = nil
}
