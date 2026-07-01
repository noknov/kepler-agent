package app

import (
	"context"

	"github.com/wati/oncall-agent/internal/config"
	"github.com/wati/oncall-agent/internal/conversation"
	"github.com/wati/oncall-agent/internal/slack"
	ttsTools "github.com/wati/oncall-agent/internal/toolkit/tools/tts"
)

func newAutoTTSFunc(cfg config.Config, slackClient *slack.Client) conversation.AutoTTSFunc {
	synth := &ttsTools.SpeakTool{
		Slack:   slackClient,
		APIKey:  cfg.Tools.TTSAPIKey,
		BaseURL: cfg.Tools.TTSBaseURL,
		Model:   cfg.Tools.TTSModel,
	}
	voice := cfg.Tools.TTSDefaultVoice
	style := cfg.Tools.TTSDefaultStyle

	return func(ctx context.Context, channel, threadTS, text string) (string, error) {
		return synth.Synthesize(ctx, channel, threadTS, text, voice, style)
	}
}

func newTTSSummarizer(cfg config.Config, runtime agentRuntime) *conversation.TTSSummarizer {
	client := runtime.Runner.LLM
	model := cfg.LLM.Model

	// Prefer the secondary/compact model for summarization (cheaper/faster).
	if runtime.Runner.Compactor != nil && runtime.Runner.Compactor.LLMClient != nil {
		client = runtime.Runner.Compactor.LLMClient
		if runtime.Runner.Compactor.CompactModel != "" {
			model = runtime.Runner.Compactor.CompactModel
		}
	}

	if client == nil {
		return nil
	}
	return &conversation.TTSSummarizer{
		Client: client,
		Model:  model,
	}
}
