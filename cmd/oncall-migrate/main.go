// oncall-migrate imports legacy JSON durable state into PostgreSQL.
// It is idempotent and never deletes source files.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/runs"
	"github.com/wati/oncall-agent/internal/session"
)

func main() {
	var sessionsDir, runsDir string
	var dryRun bool
	flag.StringVar(&sessionsDir, "sessions-dir", ".data/sessions", "legacy session JSON directory")
	flag.StringVar(&runsDir, "runs-dir", ".data/runs", "legacy run JSON directory")
	flag.BoolVar(&dryRun, "dry-run", false, "validate and count records without writing PostgreSQL")
	flag.Parse()

	if dryRun {
		sessions, err := countJSON(sessionsDir)
		if err != nil {
			log.Fatal(err)
		}
		runsCount, err := countJSON(runsDir)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("dry run: sessions=%d runs=%d\n", sessions, runsCount)
		return
	}
	postgresDSN, err := migrationPostgresDSN()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	sessionStore, err := session.NewPGStore(ctx, postgresDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer sessionStore.Close()
	runStore, err := runs.NewPGStore(ctx, postgresDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer runStore.Close()
	sessions, err := importSessions(ctx, sessionStore, sessionsDir)
	if err != nil {
		log.Fatal(err)
	}
	runsCount, err := importRuns(ctx, runStore, runsDir)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("migration complete: sessions=%d runs=%d\n", sessions, runsCount)
}

func importSessions(ctx context.Context, store *session.PGStore, dir string) (int, error) {
	return importJSON(dir, func(path string, data []byte) error {
		var s session.Session
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if strings.TrimSpace(s.ID) == "" {
			return fmt.Errorf("%s: missing session id", path)
		}
		return store.Save(ctx, s)
	})
}
func importRuns(ctx context.Context, store *runs.PGStore, dir string) (int, error) {
	return importJSON(dir, func(path string, data []byte) error {
		var r runs.Run
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if strings.TrimSpace(r.ID) == "" {
			return fmt.Errorf("%s: missing run id", path)
		}
		return store.Save(ctx, r)
	})
}
func importJSON(dir string, save func(string, []byte) error) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return count, err
		}
		if err := save(path, data); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
func countJSON(dir string) (int, error) {
	return importJSON(dir, func(_ string, _ []byte) error { return nil })
}

func migrationPostgresDSN() (string, error) {
	if dsn := firstEnv("POSTGRES_DSN", "REMINDER_POSTGRES_DSN", "RAG_POSTGRES_DSN"); dsn != "" {
		return dsn, nil
	}
	values, err := readDotEnv(".env")
	if err != nil {
		return "", err
	}
	for _, key := range []string{"POSTGRES_DSN", "REMINDER_POSTGRES_DSN", "RAG_POSTGRES_DSN"} {
		if dsn := strings.TrimSpace(values[key]); dsn != "" {
			return dsn, nil
		}
	}
	return "", fmt.Errorf("POSTGRES_DSN is required for migration")
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func readDotEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			values[key] = value
		}
	}
	return values, nil
}
