package observabilitysvc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/noknov/kepler-agent/packages/config"
)

func TestOverviewAuthorizationAndWindow(t *testing.T) {
	calls := 0
	s := &Service{cfg: config.Config{Observing: config.ObservingConfig{AdminToken: "test-token"}}, overview: func(_ context.Context, start, end time.Time) (Overview, error) {
		calls++
		if end.Sub(start) != time.Hour {
			t.Errorf("window=%v", end.Sub(start))
		}
		return Overview{Runs: 3}, nil
	}}
	for _, tc := range []struct {
		window, token string
		code          int
	}{{"1h", "", 403}, {"0", "test-token", 400}, {"169h", "test-token", 400}, {"bad", "test-token", 400}, {"1h", "test-token", 200}} {
		r := httptest.NewRequest("GET", "/overview?window="+tc.window, nil)
		r.Header.Set("X-Kepler-Agent-Admin-Token", tc.token)
		w := httptest.NewRecorder()
		s.handleOverview(w, r)
		if w.Code != tc.code {
			t.Fatalf("window %q got %d: %s", tc.window, w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("unauthorized or invalid query reached database: %d calls", calls)
	}
	w := httptest.NewRecorder()
	s.handleDashboard(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "auth-form") {
		t.Fatal("browser cannot reach login shell")
	}
}

func TestWorkerProbeNeverForwardsCredentialsOrFollowsRedirects(t *testing.T) {
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Kepler-Agent-Admin-Token") != "" {
			t.Error("forwarded private credentials")
		}
		if r.URL.Path == "/readyz" {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/metrics" {
			w.Write([]byte(`{"llm_calls":2}`))
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
	}))
	defer server.Close()
	s := &Service{cfg: config.Config{Observing: config.ObservingConfig{WorkerURL: server.URL}}}
	result := s.workerSignals(context.Background())
	if result.Ready || result.State != "available" || result.Metrics.LLMCalls != 2 {
		t.Fatalf("signals=%+v", result)
	}
	if len(paths) != 2 {
		t.Fatal(paths)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, server.URL+"/private", 302) }))
	defer redirect.Close()
	s.cfg.Observing.WorkerURL = redirect.URL
	if r := s.workerSignals(context.Background()); r.State != "metrics_unavailable" || r.Ready {
		t.Fatalf("redirect=%+v", r)
	}
	if len(paths) != 2 {
		t.Fatal("redirect was followed")
	}
	s.cfg.Observing.WorkerURL = "http://private-user:secret@example.com"
	if s.workerSignals(context.Background()).State != "invalid_worker_url" {
		t.Fatal("credential URL accepted")
	}
}

func TestOverviewSQLWindowQueuesAndMetadata(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	// Temp tables shadow public objects. This test never writes production data.
	_, err = tx.Exec(ctx, `CREATE TEMP TABLE agent_runs(id text,session_id text,started_at timestamptz,payload jsonb);
 CREATE TEMP TABLE agent_run_steps(run_id text,payload jsonb);
 CREATE TEMP TABLE agent_transcript_events(type text,at timestamptz,payload jsonb);
 CREATE TEMP TABLE slack_event_inbox(event_id text,status text,received_at timestamptz,claim_until timestamptz,dead_lettered_at timestamptz,attempt_count int,last_error text);
 CREATE TEMP TABLE agent_session_inputs(kind text,claim_until timestamptz,acknowledged_at timestamptz);`)
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	start := end.Add(-time.Hour)
	for _, run := range []struct {
		id, status string
		at         time.Time
		ms         int
	}{{"good", "completed", start, 100}, {"bad", "error", start.Add(time.Minute), 200}, {"cancel", "canceled", start.Add(time.Minute), 300}, {"boundary", "error", end, 999}, {"old", "running", start.Add(-time.Hour), 0}} {
		data, _ := json.Marshal(map[string]any{"status": run.status, "duration_ms": run.ms, "error": "summary", "messages": "private conversation", "usage": map[string]int{"total_tokens": 10}})
		if _, err = tx.Exec(ctx, `INSERT INTO agent_runs VALUES($1,'session',$2,$3::jsonb)`, run.id, run.at, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO agent_run_steps VALUES ('bad','{"type":"model","name":"test-model","error":"failed","duration_ms":150,"metadata":{"first_token_ms":25}}'),('boundary','{"type":"model","name":"excluded","duration_ms":999}');
 INSERT INTO slack_event_inbox VALUES('dead','dead_letter',$1,NULL,$1,3,'failure'),('expired','processing',$1,$1,NULL,1,''),('queued','queued',$1,NULL,NULL,0,'');
 INSERT INTO agent_session_inputs VALUES('web',$1,NULL),('queue',NULL,NULL),('web',$1,$1);
 INSERT INTO agent_transcript_events VALUES('model_attempted',$1,'{"metadata":{"outcome":"retry"}}');`, pgx.QueryExecModeSimpleProtocol, start)
	if err != nil {
		t.Fatal(err)
	}
	result, err := queryOverview(ctx, tx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs != 3 || result.FailureRate == nil || *result.FailureRate != 0.5 {
		t.Fatalf("totals=%+v", result)
	}
	if len(result.Running) != 1 || result.Running[0].ID != "old" || len(result.RecentIssues) != 1 || result.RecentIssues[0].ID != "bad" {
		t.Fatal("incorrect window or running cohort")
	}
	if result.Inbox.ExpiredClaims != 1 || result.Inbox.OldestQueuedMS != 3600000 || result.Inputs["web:expired"] != 1 || result.Inputs["queue:queued"] != 1 || len(result.DeadLetters) != 1 {
		t.Fatalf("queue/attempt totals=%+v", result)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "private conversation") {
		t.Fatal("overview leaked full run content")
	}
	empty, err := queryOverview(ctx, tx, end.Add(time.Hour), end.Add(2*time.Hour))
	if err != nil || empty.Runs != 0 || empty.FailureRate != nil {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}
