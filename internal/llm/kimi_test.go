package llm

import "testing"

func TestMiMoChatBodyUsesOfficialOpenAIFields(t *testing.T) {
	client := NewKimiClient("https://api.xiaomimimo.com/v1", "token", 0)
	body := client.chatBody(Request{
		Model:     "mimo-v2.5",
		Messages:  []Message{{Role: "user", Content: "hello"}},
		MaxTokens: 1234,
		Thinking:  "disabled",
	})

	if _, ok := body["max_tokens"]; ok {
		t.Fatal("MiMo request should not use max_tokens")
	}
	if got := body["max_completion_tokens"]; got != 1234 {
		t.Fatalf("max_completion_tokens = %#v, want 1234", got)
	}
	thinking, ok := body["thinking"].(map[string]string)
	if !ok {
		t.Fatalf("thinking = %#v, want map[string]string", body["thinking"])
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking.type = %q, want disabled", thinking["type"])
	}
}

func TestBearerTokenValue(t *testing.T) {
	if got := bearerTokenValue("Bearer sk-test"); got != "sk-test" {
		t.Fatalf("bearerTokenValue() = %q, want sk-test", got)
	}
	if got := bearerTokenValue("sk-test"); got != "sk-test" {
		t.Fatalf("bearerTokenValue() = %q, want sk-test", got)
	}
}

func TestProviderName(t *testing.T) {
	if got := NewKimiClient("https://api.xiaomimimo.com/v1", "token", 0).providerName(); got != "mimo" {
		t.Fatalf("providerName() = %q, want mimo", got)
	}
	if got := NewKimiClient("https://api.moonshot.ai/v1", "token", 0).providerName(); got != "kimi" {
		t.Fatalf("providerName() = %q, want kimi", got)
	}
}
