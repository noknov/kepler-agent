package appsupport

import (
	"context"

	"github.com/noknov/slack-copilot-agent/packages/config"
	"github.com/noknov/slack-copilot-agent/packages/conversation"
	appruntime "github.com/noknov/slack-copilot-agent/packages/runtime"
	"github.com/noknov/slack-copilot-agent/packages/slack"
	ttsTools "github.com/noknov/slack-copilot-agent/packages/toolkit/tools/tts"
)

func NewAutoTTSFunc(cfg config.Config, slackClient *slack.Client) conversation.AutoTTSFunc {
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

func NewTTSSummarizer(cfg config.Config, runtime appruntime.AgentRuntime) *conversation.TTSSummarizer {
	client := runtime.Runner.LLM
	model := cfg.LLM.Model

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
