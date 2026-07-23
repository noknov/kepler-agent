package conversation

import (
	"context"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/noknov/slack-copilot-agent/internal/llm"
)

// AutoTTSFunc synthesizes text to speech and uploads the audio to a Slack thread.
// It returns the file permalink or an error. Implementations should be safe
// for concurrent goroutine use.
type AutoTTSFunc func(ctx context.Context, channel, threadTS, text string) (string, error)

// TTSSummarizer condenses long agent output into a brief spoken summary.
type TTSSummarizer struct {
	Client llm.Client
	Model  string
}

const ttsSummaryPrompt = `你是一个语音播报助手。请将以下 AI 回复内容总结为一段简短的口语化摘要，用于语音朗读。

要求：
- 保留关键信息和结论，去掉代码、链接、表格等不适合朗读的内容
- 用自然、口语化的中文表达（如果原文是英文，也翻译为中文）
- 长度控制在 2-4 句话，不超过 150 字
- 直接输出摘要内容，不要加前缀如"摘要："

原文：
`

// Summarize uses the LLM to condense text into a brief spoken summary.
func (s *TTSSummarizer) Summarize(ctx context.Context, text string) (string, error) {
	if s == nil || s.Client == nil {
		return text, nil
	}
	resp, err := s.Client.Chat(ctx, llm.Request{
		Model: s.Model,
		Messages: []llm.Message{
			{Role: "user", Content: ttsSummaryPrompt + text},
		},
		MaxTokens:   300,
		Temperature: 0.3,
	})
	if err != nil {
		return "", err
	}
	summary := strings.TrimSpace(resp.Message.Content)
	if summary == "" {
		return text, nil
	}
	return summary, nil
}

// maybeAutoTTS fires the TTS hook in a background goroutine if configured.
// Only fires in DM channels (channel ID starts with "D").
// For long text, uses the summarizer to create a brief spoken version first.
func (s *Service) maybeAutoTTS(channel, threadTS, text string) {
	if s.AutoTTS == nil {
		return
	}
	if !strings.HasPrefix(channel, "D") {
		return
	}
	text = stripMarkdownForTTS(text)
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	summarizer := s.TTSSummarizer
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		spokenText := prepareTTSText(ctx, text, summarizer)
		if spokenText == "" {
			return
		}
		if _, err := s.AutoTTS(ctx, channel, threadTS, spokenText); err != nil {
			log.Printf("auto-tts: %v", err)
		}
	}()
}

const (
	// Short enough to speak directly without summarization.
	ttsDirectMaxChars = 200
	// Hard cap for the final spoken text sent to TTS API.
	ttsHardMaxChars = 500
)

// prepareTTSText produces spoken text: short replies pass through directly,
// longer ones get summarized by the LLM.
func prepareTTSText(ctx context.Context, text string, summarizer *TTSSummarizer) string {
	if text == "" {
		return ""
	}
	charCount := utf8.RuneCountInString(text)

	if charCount <= ttsDirectMaxChars {
		return text
	}

	// Use LLM summarizer for long text
	if summarizer != nil {
		summary, err := summarizer.Summarize(ctx, text)
		if err != nil {
			log.Printf("auto-tts summarize: %v", err)
			// Fall back to truncation
		} else if summary != "" {
			return enforceMaxChars(summary, ttsHardMaxChars)
		}
	}

	// Fallback: truncate
	return enforceMaxChars(text, ttsHardMaxChars)
}

func enforceMaxChars(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "……"
}

// stripMarkdownForTTS removes code blocks, inline code, image links, and other
// formatting that doesn't make sense in spoken form.
func stripMarkdownForTTS(text string) string {
	lines := strings.Split(text, "\n")
	var result []string
	inCodeBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}
		if strings.HasPrefix(trimmed, "![") {
			continue
		}
		if strings.HasPrefix(trimmed, "| ") && strings.Contains(trimmed, " | ") {
			continue
		}
		if strings.HasPrefix(trimmed, "|---") || strings.HasPrefix(trimmed, "| ---") {
			continue
		}
		if strings.HasPrefix(trimmed, "_") && strings.HasSuffix(trimmed, "_") && strings.Contains(trimmed, "token") {
			continue
		}
		result = append(result, line)
	}
	text = strings.Join(result, "\n")
	text = strings.ReplaceAll(text, "`", "")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	for strings.Contains(text, "# ") {
		idx := strings.Index(text, "# ")
		if idx == 0 || text[idx-1] == '\n' || text[idx-1] == '#' {
			text = text[:idx] + text[idx+2:]
		} else {
			break
		}
	}
	return text
}
