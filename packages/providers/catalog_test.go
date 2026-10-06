package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/llm"
)

func TestResolveKnownModelCapabilities(t *testing.T) {
	cases := []struct {
		model    string
		protocol string
		image    bool
	}{
		{model: "grok-4.6", protocol: "responses", image: true},
		{model: "gpt-5.6-luna", protocol: "responses", image: true},
		{model: "deepseek-v4.1-flash", protocol: "openai", image: true},
		{model: "glm-5.2", protocol: "openai", image: false},
	}
	for _, test := range cases {
		capabilities, ok := ResolveModel("opencode-go", test.model)
		if !ok {
			t.Fatalf("ResolveModel(%q) returned unknown", test.model)
		}
		if capabilities.Protocol != test.protocol || capabilities.SupportsInput(ModalityImage) != test.image {
			t.Fatalf("capabilities(%q) = %+v, want protocol=%q image=%t", test.model, capabilities, test.protocol, test.image)
		}
	}
}

func TestResponsesModels(t *testing.T) {
	got := responsesModels("opencode-go")
	want := []string{"grok-4.6", "gpt-5.6-luna"}
	if len(got) != len(want) {
		t.Fatalf("responsesModels() = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("responsesModels()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

func TestSupportsInputRequiresCatalogCapability(t *testing.T) {
	if SupportsInput("opencode-go", "glm-5.2", ModalityImage) {
		t.Fatal("known text-only model accepted image input")
	}
	if SupportsInput("custom", "vision-model", ModalityImage) {
		t.Fatal("unknown model accepted image input")
	}
}

func TestResolveDeepSeekModelCapabilities(t *testing.T) {
	cases := []struct {
		model    string
		protocol string
		image    bool
	}{
		{model: "deepseek-flash", protocol: "openai", image: true},
		{model: "deepseek-v4-flash", protocol: "openai", image: true},
		{model: "deepseek-v4-flash-vision-exp", protocol: "openai", image: true},
		{model: "deepseek-v4-pro", protocol: "openai", image: false},
	}
	for _, test := range cases {
		capabilities, ok := ResolveModel("deepseek", test.model)
		if !ok {
			t.Fatalf("ResolveModel(%q) returned unknown", test.model)
		}
		if capabilities.Protocol != test.protocol || capabilities.SupportsInput(ModalityImage) != test.image {
			t.Fatalf("capabilities(%q) = %+v, want protocol=%q image=%t", test.model, capabilities, test.protocol, test.image)
		}
	}
	if got := responsesModels("deepseek"); len(got) != 0 {
		t.Fatalf("responsesModels(deepseek) = %#v, want none", got)
	}
}

func TestQoderCapabilitiesUseAccountCatalogByKeyAndDisplayName(t *testing.T) {
	client, err := New(Config{Provider: "qoder", Protocol: "qoder", BaseURL: llm.QoderBaseURL, APIKey: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	catalogs := 0
	client.Wire.(*llm.QoderClient).SetTransport(nativeQoderTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/api/v1/jobToken/exchange":
			body = fmt.Sprintf(`{"token":"job","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		case "/api/v1/userinfo":
			body = `{"id":"user"}`
		case "/algo/api/v2/model/list":
			catalogs++
			body = `{"assistant":[{"key":"opaque-vision-id","display_name":"Account Vision","enable":true,"is_vl":true},{"key":"text","enable":true,"is_vl":false},{"key":"disabled","enable":false,"is_vl":true}]}`
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	for _, test := range []struct {
		name           string
		image, invalid bool
	}{
		{"opaque-vision-id", true, false}, {"Account Vision", true, false}, {"text", false, false}, {"disabled", false, true}, {"unknown", false, true},
	} {
		caps, err := client.Capabilities(context.Background(), "qoder", test.name)
		if (err != nil) != test.invalid || caps.SupportsInput(ModalityImage) != test.image {
			t.Fatalf("%s: %+v err=%v", test.name, caps, err)
		}
	}
	if catalogs != 1 {
		t.Fatalf("catalog requests=%d; expected inference cache reuse", catalogs)
	}
}
