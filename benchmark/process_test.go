package benchmark

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/cloud"
	"github.com/noknov/kepler-agent/packages/providers"
)

type benchmarkFixtureModel struct {
	started chan struct{}
	blocked bool
}

func (m benchmarkFixtureModel) Generate(ctx context.Context, _ model.Request, sink model.EventSink) (model.Response, error) {
	if m.started != nil {
		close(m.started)
	}
	if m.blocked {
		<-ctx.Done()
		return model.Response{}, ctx.Err()
	}
	_ = sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "OK"})
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "OK"), FinishReason: model.FinishStop, Usage: model.Usage{InputTokens: 3, OutputTokens: 1}}, nil
}

func TestBenchmarkProcessHelper(t *testing.T) {
	if os.Getenv("KEPLER_BENCHMARK_TEST_CHILD") != "1" {
		return
	}
	flag.CommandLine = flag.NewFlagSet("kepler-bench", flag.ContinueOnError)
	os.Args = []string{"kepler-bench", "--cwd", os.Getenv("KEPLER_BENCHMARK_TEST_WORKSPACE"), "--state-dir", os.Getenv("KEPLER_BENCHMARK_TEST_STATE"), "--config", os.Getenv("KEPLER_BENCHMARK_TEST_CONFIG"), "Inspect the fixture"}
	if err := Run(); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	os.Exit(0)
}

func TestBenchmarkProcessEmitsCanonicalJSONAndCancelsOnSIGTERM(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "completion", true: "cancellation"}[blocked], func(t *testing.T) {
			started := make(chan struct{})
			fixture := benchmarkFixtureModel{started: started, blocked: blocked}
			mux := http.NewServeMux()
			mux.HandleFunc("/benchmark/bootstrap", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Error("missing benchmark authentication")
				}
				json.NewEncoder(w).Encode(cloud.Bootstrap{Provider: "fixture", Protocol: "kepler", Model: "fixture"})
			})
			mux.Handle(providers.KeplerGeneratePath, cloud.HandleHostedGenerate(fixture, cloud.HostedGeneratePolicy{Model: "fixture", MaxOutputTokens: 4096}))
			server := httptest.NewServer(mux)
			defer server.Close()
			root := t.TempDir()
			config := filepath.Join(root, "config.toml")
			if err := os.WriteFile(config, []byte("output = 'jsonl'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(root, "workspace")
			os.Mkdir(workspace, 0700)
			command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestBenchmarkProcessHelper$")
			command.Env = append(os.Environ(), "KEPLER_BENCHMARK_TEST_CHILD=1", "KEPLER_API_URL="+server.URL, "KEPLER_TOKEN=fixture-token", "KEPLER_BENCHMARK_TEST_WORKSPACE="+workspace, "KEPLER_BENCHMARK_TEST_STATE="+filepath.Join(root, "state"), "KEPLER_BENCHMARK_TEST_CONFIG="+config)
			var output, diagnostics strings.Builder
			command.Stdout = &output
			command.Stderr = &diagnostics
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				command.Process.Kill()
				command.Wait()
				t.Fatalf("benchmark did not dispatch: %s", diagnostics.String())
			}
			if blocked {
				if err := command.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				command.Process.Kill()
				<-done
				t.Fatal("benchmark did not complete bounded cleanup")
			}
			if (err != nil) != blocked {
				t.Fatalf("exit=%v stderr=%s", err, diagnostics.String())
			}
			scanner := bufio.NewScanner(strings.NewReader(output.String()))
			terminal := transcript.EventType("")
			for scanner.Scan() {
				var event transcript.Event
				if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
					t.Fatalf("stdout contains non-protocol text: %q", scanner.Text())
				}
				if event.Type.IsTurnTerminal() {
					terminal = event.Type
				}
			}
			expected := transcript.TurnCompleted
			if blocked {
				expected = transcript.TurnCanceled
			}
			if terminal != expected {
				t.Fatalf("terminal=%s want=%s stderr=%s", terminal, expected, diagnostics.String())
			}
		})
	}
}
