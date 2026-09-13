package providers

import "testing"

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
