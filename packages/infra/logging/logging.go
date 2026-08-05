package logging

import (
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"sync"
)

var configureOnce sync.Once

// Configure installs one JSON logger for slog and standard-library log calls,
// keeping every service machine-parseable.
func Configure(levelText string) {
	configureOnce.Do(func() {
		level := new(slog.LevelVar)
		switch strings.ToLower(strings.TrimSpace(levelText)) {
		case "debug":
			level.Set(slog.LevelDebug)
		case "warn", "warning":
			level.Set(slog.LevelWarn)
		case "error":
			level.Set(slog.LevelError)
		default:
			level.Set(slog.LevelInfo)
		}
		handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
		slog.SetDefault(slog.New(handler))
		log.SetFlags(0)
		log.SetOutput(slogWriter{})
	})
}

type slogWriter struct{}

func (slogWriter) Write(p []byte) (int, error) {
	message := strings.TrimSpace(string(p))
	if message != "" {
		slog.Default().Log(context.Background(), slog.LevelInfo, message)
	}
	return len(p), nil
}

var _ io.Writer = slogWriter{}
