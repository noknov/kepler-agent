package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/llm"
)

func TestPipelineBuildActiveRequestKeepsExternalEvidenceVerbatim(t *testing.T) {
	thread := "U1: first report\n" + strings.Repeat("U2: noisy middle\n", 50) + "U3: final clarification"
	p := NewPipeline(Builder{}, nil)

	active := p.BuildActiveRequest(ActiveRequestInput{
		SystemPrompt: "system",
		ExternalEvidence: []ExternalEvidence{{
			Source:  "slack_thread",
			Content: thread,
		}},
		UserText:       "what happened?",
		SessionSummary: "older agent summary",
		Turns:          []Turn{UserTurn("previous user"), {Role: RoleAssistant, Content: "previous assistant"}},
	})

	if len(active.Messages) < 4 {
		t.Fatalf("messages = %#v", active.Messages)
	}
	threadMessage := active.Messages[2].Content
	if !strings.Contains(threadMessage, thread) {
		t.Fatalf("external evidence was not preserved verbatim: %q", threadMessage)
	}
	if active.ExternalEvidenceTokens <= 0 {
		t.Fatalf("ExternalEvidenceTokens = %d, want positive", active.ExternalEvidenceTokens)
	}
}

func TestPipelineCompactSessionConversationExcludesThreadContext(t *testing.T) {
	client := &countingCompactClient{}
	p := NewPipeline(Builder{}, &Compactor{
		MaxContextTokens:    80,
		AutocompactBuffer:   10,
		OutputReserve:       10,
		MaxToolResultTokens: 10,
		LLMClient:           client,
		CompactModel:        "compact-test",
	})
	turns := []Turn{
		UserTurn(strings.Repeat("old agent conversation ", 80)),
		{Role: RoleAssistant, Content: strings.Repeat("old answer ", 80)},
	}

	result := p.CompactSessionConversation(context.Background(), turns, "")
	if result.Layer != "llm_compact" {
		t.Fatalf("Layer = %q, want llm_compact", result.Layer)
	}
	if client.calls != 1 {
		t.Fatalf("compact calls = %d, want 1", client.calls)
	}
	for _, turn := range result.Turns {
		if strings.Contains(turn.Content, "slack_thread_context") {
			t.Fatalf("thread context leaked into session compact result: %#v", result.Turns)
		}
	}
}

var _ llm.Client = (*countingCompactClient)(nil)
