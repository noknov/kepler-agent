package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
)

type Uploader interface {
	UploadFile(ctx context.Context, channel, threadTS, filename string, data []byte) (string, error)
}

type SpeakTool struct {
	Slack      Uploader
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

func (SpeakTool) IsWrite() bool  { return true }

func (t SpeakTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(
		"tts-speak",
		"",
		tool.ObjectSchema([]string{"text"}, map[string]any{
			"text":  map[string]any{"type": "string", "description": ""},
			"voice": map[string]any{"type": "string", "description": ""},
			"style": map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t SpeakTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
		Style string `json:"style"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Text == "" {
		return tool.Result{}, fmt.Errorf("text is required")
	}
	if args.Voice == "" {
		args.Voice = "冰糖"
	}

	permalink, err := t.Synthesize(ctx, call.Scope.Values["channel"], call.Scope.Values["thread_ts"], args.Text, args.Voice, args.Style)
	if err != nil {
		return tool.Result{}, err
	}

	return tool.TextResult(fmt.Sprintf("Voice message sent successfully (voice: %s).\nPermalink: %s", args.Voice, permalink)), nil
}

// Synthesize generates speech from text and uploads the audio to a Slack thread.
// Returns the file permalink.
func (t SpeakTool) Synthesize(ctx context.Context, channel, threadTS, text, voice, style string) (string, error) {
	audioData, err := t.synthesize(ctx, text, voice, style)
	if err != nil {
		return "", err
	}

	permalink, err := t.Slack.UploadFile(ctx, channel, threadTS, "voice.wav", audioData)
	if err != nil {
		return "", fmt.Errorf("failed to upload audio to Slack: %w", err)
	}
	return permalink, nil
}

func (t SpeakTool) synthesize(ctx context.Context, text, voice, style string) ([]byte, error) {
	messages := make([]map[string]string, 0, 2)
	if style != "" {
		messages = append(messages, map[string]string{
			"role":    "user",
			"content": style,
		})
	}
	messages = append(messages, map[string]string{
		"role":    "assistant",
		"content": text,
	})

	model := t.Model
	if model == "" {
		model = "mimo-v2.5-tts"
	}

	body := map[string]any{
		"model":    model,
		"messages": messages,
		"audio": map[string]string{
			"format": "wav",
			"voice":  voice,
		},
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	baseURL := t.BaseURL
	if baseURL == "" {
		baseURL = "https://api.xiaomimimo.com/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api-key", t.APIKey)

	client := t.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("TTS API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("TTS API response read failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TTS API returned status %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Audio struct {
					Data string `json:"data"`
				} `json:"audio"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("TTS API response parse failed: %w", err)
	}
	if len(result.Choices) == 0 || result.Choices[0].Message.Audio.Data == "" {
		return nil, fmt.Errorf("TTS API returned no audio data")
	}

	audioBytes, err := base64.StdEncoding.DecodeString(result.Choices[0].Message.Audio.Data)
	if err != nil {
		return nil, fmt.Errorf("TTS audio base64 decode failed: %w", err)
	}
	return audioBytes, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
