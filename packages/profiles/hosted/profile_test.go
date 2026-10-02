package hosted

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/providers"
)

type failingRoute struct{ calls int }

func (c *failingRoute) Generate(context.Context, model.Request, model.EventSink) (model.Response, error) {
	c.calls++
	return model.Response{}, &model.Error{Kind: model.ErrorTransient, Retryable: true, Message: "unavailable"}
}

func TestFallbackDoesNotRepeatIdenticalRoute(t *testing.T) {
	for _, distinct := range []string{"", "model", "endpoint", "credentials", "protocol", "provider", "flavor", "uppercase flavor"} {
		t.Run(distinct, func(t *testing.T) {
			cfg := config.Config{LLM: config.LLMConfig{
				Provider: "provider", SecondaryProvider: "provider", Model: "model", SecondaryModel: "model",
				Protocol: "anthropic", SecondaryProtocol: "anthropic", BaseURL: "https://example.test", SecondaryBaseURL: "https://example.test",
				APIKey: "synthetic-key", SecondaryAPIKey: "synthetic-key", Resilience: config.ResilienceConfig{MaxAttempts: 1},
			}}
			switch distinct {
			case "model":
				cfg.LLM.SecondaryModel = "other"
			case "endpoint":
				cfg.LLM.SecondaryBaseURL = "https://other.test"
			case "credentials":
				cfg.LLM.SecondaryAPIKey = "other-synthetic-key"
			case "protocol":
				cfg.LLM.SecondaryProtocol = "responses"
			case "provider":
				cfg.LLM.SecondaryProvider = "other-provider"
			case "flavor":
				cfg.LLM.AnthropicFlavor = "other-flavor"
			case "uppercase flavor":
				cfg.LLM.Protocol, cfg.LLM.SecondaryProtocol = "ANTHROPIC", "ANTHROPIC"
				cfg.LLM.AnthropicFlavor = "other-flavor"
			}
			primary, fallback := &failingRoute{}, &failingRoute{}
			client := resilientModel(cfg, primary, cfg.LLM.Provider, fallback, cfg.LLM.SecondaryProvider, cfg.LLM.SecondaryModel)
			_, err := client.Generate(context.Background(), model.Request{Model: cfg.LLM.Model}, nil)
			var typed *model.Error
			if !errors.As(err, &typed) || primary.calls != 1 {
				t.Fatalf("error=%v primary=%d", err, primary.calls)
			}
			wantFallback := 1
			if distinct == "" {
				wantFallback = 0
			}
			if fallback.calls != wantFallback {
				t.Fatalf("fallback calls=%d, want %d", fallback.calls, wantFallback)
			}
		})
	}
}

func TestSecondaryModelClientPreservesSharedProviderProtocolRouting(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`))
	}))
	defer server.Close()

	client, modelName, err := secondaryModelClient(config.Config{LLM: config.LLMConfig{
		Provider:          "opencode-go",
		SecondaryProvider: "opencode-go",
		SecondaryProtocol: "openai",
		SecondaryBaseURL:  server.URL,
		SecondaryModel:    "gpt-5.6-luna",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if modelName != "gpt-5.6-luna" {
		t.Fatalf("model name = %q, want gpt-5.6-luna", modelName)
	}
	providerClient, ok := client.(*providers.Client)
	if !ok {
		t.Fatalf("client type = %T, want *providers.Client", client)
	}
	if _, err := providerClient.Generate(context.Background(), model.Request{Model: modelName}, nil); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
}
