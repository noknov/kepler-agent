package hosted

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/config"
	"github.com/noknov/kepler-agent/packages/providers"
)

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
		ResponsesModels:   []string{"gpt-5.6-luna"},
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
