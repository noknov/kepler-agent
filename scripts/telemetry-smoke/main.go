// telemetry-smoke verifies configured OTLP delivery using synthetic data only.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	"github.com/noknov/kepler-agent/packages/agent/runtime"
	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/agent/transcript"
	"github.com/noknov/kepler-agent/packages/infra/telemetry"
)

type syntheticModel struct{}

func (syntheticModel) Generate(_ context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	toolDone := false
	for _, message := range request.Messages {
		if message.Role == model.RoleTool {
			toolDone = true
		}
	}
	if !toolDone {
		return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: []model.Content{{Type: model.ContentToolCall, ToolCall: &model.ToolCall{ID: "smoke-call", Name: "synthetic_echo", Arguments: json.RawMessage(`{"query":"synthetic SELECT 1","api_key":"synthetic-private-key"}`)}}}}, FinishReason: model.FinishToolCalls, Usage: model.Usage{Reported: true, InputTokens: 10, OutputTokens: 2, CacheReadTokens: 6, CacheTokensIncludedInInput: true}}, nil
	}
	if sink != nil {
		if err := sink(model.StreamEvent{Type: model.StreamTextDelta, Text: "synthetic smoke"}); err != nil {
			return model.Response{}, err
		}
	}
	return model.Response{Message: model.TextMessage(model.RoleAssistant, "synthetic smoke"), FinishReason: model.FinishStop, Usage: model.Usage{Reported: true, InputTokens: 10, OutputTokens: 2, CacheReadTokens: 6, CacheTokensIncludedInInput: true, ReasoningTokens: 1}}, nil
}

type syntheticUnavailable struct{}

func (syntheticUnavailable) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	return model.Response{}, &model.Error{Kind: model.ErrorUnavailable, Retryable: true, Message: "synthetic failure with private-error-sentinel"}
}

type syntheticTool struct{}

func (syntheticTool) Descriptor() tool.Descriptor {
	return tool.Descriptor{Name: "synthetic_echo", InputSchema: json.RawMessage(`{"type":"object"}`), Effects: []tool.Effect{tool.EffectRead}}
}
func (syntheticTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	return tool.TextResult("synthetic tool result"), nil
}

func run() error {
	file := flag.String("env-file", "", "private OTLP/Langfuse env file (never evaluated as shell)")
	flag.Parse()
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return fmt.Errorf("cannot read telemetry env file")
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			allowed := strings.HasPrefix(key, "OTEL_") || key == "LANGFUSE_BASE_URL" || key == "LANGFUSE_PUBLIC_KEY" || key == "LANGFUSE_SECRET_KEY"
			if !ok || !allowed || strings.ContainsAny(value, "\r\x00") {
				return fmt.Errorf("invalid telemetry env file")
			}
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("invalid telemetry env key")
			}
		}
		if scanner.Err() != nil {
			return fmt.Errorf("cannot read telemetry env file")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdown, err := telemetry.Setup(ctx, "kepler-agent-smoke")
	if err != nil {
		return err
	}
	if !telemetry.CurrentStatus().Configured {
		return fmt.Errorf("trace export is not configured")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(cleanup)
	}()
	catalog, _ := tool.NewCatalog(syntheticTool{})
	store := transcript.NewMemoryStore()
	client := &model.ResilientClient{Primary: syntheticUnavailable{}, PrimaryProvider: "synthetic-primary", Fallback: syntheticModel{}, FallbackProvider: "synthetic-backup", FallbackModel: "synthetic-smoke", MaxAttempts: 1, MinAttemptBudget: time.Millisecond}
	runner, err := runtime.New(runtime.Config{Model: "synthetic-requested"}, runtime.Dependencies{TraceContent: telemetry.ContentRecorder(), Model: client, Tools: catalog, Transcript: store})
	if err != nil {
		return err
	}
	// Deployment smoke is explicitly separated from real customer traffic.
	id := fmt.Sprintf("smoke-%d", time.Now().UnixNano())
	_, err = runner.RunTurn(ctx, runtime.TurnRequest{SessionID: id, TurnID: id, Input: model.TextMessage(model.RoleUser, "synthetic smoke"), Prompt: []prompt.Fragment{{ID: "smoke", Layer: prompt.LayerCore, Content: "Synthetic observation test only."}}, Scope: tool.Scope{UserID: "kepler-smoke", Values: map[string]string{"surface": "smoke"}}})
	if err != nil {
		return fmt.Errorf("synthetic runtime failed")
	}
	events, err := store.Load(ctx, id, 0)
	if err != nil {
		return err
	}
	traceID := ""
	for _, event := range events {
		if event.Trace != nil {
			traceID = event.Trace.TraceID
			break
		}
	}
	if err := shutdown(ctx); err != nil {
		return fmt.Errorf("OTLP export failed; check endpoint, credentials and connectivity")
	}
	state := telemetry.CurrentStatus()
	if state.ExportedBatches == 0 || state.FailedBatches > 0 {
		return fmt.Errorf("OTLP batch delivery was not successful")
	}
	fmt.Printf("OTLP accepted synthetic trace %s (%d batch). Verify this trace in the target Langfuse project.\n", traceID, state.ExportedBatches)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
