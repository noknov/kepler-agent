package memory

import (
	"context"
	"strings"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
)

const (
	// MaxCompactOutputTokens is the max output tokens for the compact summary call.
	// claude-code uses 20,000 (p99.99 of compact summary output is 17,387 tokens).
	// Raised to 32,000 for more detailed summaries.
	MaxCompactOutputTokens = 32_000
)

// CompactSystemPrompt returns the system prompt for the compact summary LLM call.
// It includes a strict no-tools preamble (matching claude-code's NO_TOOLS_PREAMBLE)
// and the structured summary prompt with 9 required sections.
func CompactSystemPrompt() string {
	fallback := `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools.
- Tool calls will be REJECTED and will waste your only turn.
- Your entire response must be plain text: an <analysis> block followed by a <summary> block.

You are a helpful AI assistant tasked with summarizing conversations.
Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.

Before providing your final summary, wrap your analysis in <analysis> tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:
1. Chronologically analyze each message and section of the conversation.
2. Double-check for technical accuracy and completeness.

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents.
2. Key Technical Concepts: List all important technical concepts, technologies, and frameworks discussed.
3. Files and Code Sections: Enumerate specific files and code sections examined, modified, or created.
4. Errors and fixes: List all errors encountered and how they were fixed. Pay special attention to user feedback about doing things differently.
5. Problem Solving: Document problems solved and any ongoing troubleshooting efforts.
6. All user messages: List ALL user messages that are not tool results.
7. Pending Tasks: Outline any pending tasks explicitly asked to work on.
8. Current Work: Describe precisely what was being worked on immediately before this summary, with file names and code snippets where applicable.
9. Optional Next Step: List the next step related to the most recent work. Include direct quotes showing exactly what task was being worked on and where it was left off.

Format your output as:
<analysis>
[Your thought process]
</analysis>

<summary>
1. Primary Request and Intent:
   [Detailed description]
...
</summary>`
	return prompts.PromptText("compact_system", fallback)
}

// FormatCompactUserMessage wraps a compact summary into a continuation message
// that the model will see at the start of the next conversation segment.
func FormatCompactUserMessage(summary string) string {
	fallbackPrefix := "This session is being continued from a previous conversation that ran out of context. The conversation is summarized below.\n\n"
	prefix := prompts.PromptText("compact_user_prefix", fallbackPrefix)
	return prefix + summary + "\n\nContinue the conversation from where it left off without asking the user any further questions."
}

// GenerateCompactSummary calls the LLM to produce a structured summary of the
// conversation. It strips the <analysis> scratchpad and keeps only <summary>.
// The thinking parameter is disabled to save tokens on the summary call.
func GenerateCompactSummary(ctx context.Context, client llm.Client, model string, messages []llm.Message, customInstructions string) (string, error) {
	// Build the compact request: system + conversation as user context + prompt
	compactMessages := buildCompactMessages(messages, customInstructions)

	req := llm.Request{
		Model:       model,
		Messages:    compactMessages,
		MaxTokens:   MaxCompactOutputTokens,
		Temperature: 0,
		// Thinking is intentionally disabled (empty string) to save tokens.
	}

	resp, err := client.Chat(ctx, req)
	if err != nil {
		return "", err
	}

	summary := llm.StripTextualToolCallMarkup(extractSummary(resp.Message.Content))
	return summary, nil
}

// buildCompactMessages prepares the message list for the compact summary call.
// The full conversation is injected as user context so the LLM can see everything.
func buildCompactMessages(messages []llm.Message, customInstructions string) []llm.Message {
	// System prompt
	sysPrompt := CompactSystemPrompt()
	if customInstructions != "" {
		sysPrompt += "\n\nAdditional instructions from the user:\n" + customInstructions
	}

	// Build a user message containing the entire conversation as context
	var conversation strings.Builder
	conversation.WriteString("<conversation_to_summarize>\n")
	for _, msg := range messages {
		role := msg.Role
		switch role {
		case "system":
			// Skip system prompts — they're not part of the conversation
			continue
		case "assistant":
			conversation.WriteString("[Assistant]")
			if msg.Content != "" {
				conversation.WriteString("\n")
				conversation.WriteString(msg.Content)
			}
			for _, tc := range msg.ToolCalls {
				conversation.WriteString("\n  → tool_call: " + tc.Function.Name)
				if tc.Function.Arguments != "" {
					conversation.WriteString("(" + tc.Function.Arguments + ")")
				}
			}
			conversation.WriteString("\n\n")
		case "tool":
			conversation.WriteString("[Tool:" + msg.Name + "]")
			conversation.WriteString("\n" + msg.Content + "\n\n")
		case "user":
			conversation.WriteString("[User]\n")
			conversation.WriteString(msg.Content)
			conversation.WriteString("\n\n")
		}
	}
	conversation.WriteString("</conversation_to_summarize>\n\n")
	conversation.WriteString("Please provide your analysis and summary as instructed.")

	return []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: conversation.String()},
	}
}

// extractSummary strips the <analysis> block and returns only the <summary> content.
func extractSummary(text string) string {
	text = strings.TrimSpace(text)

	// Remove <analysis>...</analysis> block
	if idx := strings.Index(text, "<analysis>"); idx >= 0 {
		endIdx := strings.Index(text, "</analysis>")
		if endIdx > idx {
			text = text[:idx] + text[endIdx+len("</analysis>"):]
		}
	}

	// Extract <summary>...</summary> content
	if idx := strings.Index(text, "<summary>"); idx >= 0 {
		endIdx := strings.Index(text, "</summary>")
		if endIdx > idx {
			return strings.TrimSpace(text[idx+len("<summary>") : endIdx])
		}
	}

	// Fallback: return the full text as-is
	return strings.TrimSpace(text)
}
