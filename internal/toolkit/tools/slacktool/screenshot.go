package slacktool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/noknov/slack-copilot-agent/internal/llm"
	playwrightTools "github.com/noknov/slack-copilot-agent/internal/toolkit/tools/playwright"
	"github.com/noknov/slack-copilot-agent/internal/toolkit/tools/registry"
)

// Uploader can upload a file to a Slack channel thread.
type Uploader interface {
	UploadFile(ctx context.Context, channel, threadTS, filename string, data []byte) (string, error)
}

// SendScreenshotTool uploads a browser screenshot (data URI from pw-screenshot)
// to the current Slack thread so the user can see it directly.
type SendScreenshotTool struct {
	Slack Uploader
}

func (t SendScreenshotTool) Spec() llm.ToolSpec {
	// data_uri is optional: if omitted, the tool fetches the latest screenshot from
	// RuntimeCache (set by pw-screenshot). This avoids passing multi-MB base64
	// strings through LLM context.
	return registry.FunctionSpec(
		"slack-send_screenshot",
		"Upload the most recent Playwright browser screenshot to the current Slack thread. "+
			"Only call this tool after pw-screenshot has been called; do NOT call it speculatively "+
			"or as a general-purpose screen-capture — it only works inside a Playwright automation flow.",
		registry.ObjectSchema(nil, map[string]any{
			"data_uri": map[string]any{"type": "string", "description": "Optional base64 data URI of the screenshot; omit to use the latest pw-screenshot result from cache."},
			"filename": map[string]any{"type": "string", "description": "Optional filename for the uploaded image (e.g. 'result.png')."},
		}),
	)
}

func (t SendScreenshotTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
	var args struct {
		DataURI  string `json:"data_uri"`
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	dataURI := args.DataURI
	// If the caller did not supply a data URI, fall back to the latest screenshot
	// stored in RuntimeCache by pw-screenshot.
	if dataURI == "" {
		if cached, ok := rt.Cache.Get(playwrightTools.ScreenshotCacheKey); ok {
			if s, ok := cached.(string); ok {
				dataURI = s
			}
		}
	}
	if dataURI == "" {
		return registry.Result{}, fmt.Errorf("no screenshot available: call pw-screenshot first, or provide data_uri")
	}

	imgData, mimeType, err := parseDataURI(dataURI)
	if err != nil {
		return registry.Result{}, fmt.Errorf("invalid data_uri: %w", err)
	}

	filename := strings.TrimSpace(args.Filename)
	if filename == "" {
		filename = filenameForMIME(mimeType)
	}

	channel := rt.Channel
	threadTS := rt.ThreadTS
	if channel == "" {
		return registry.Result{}, fmt.Errorf("slack channel not available in this runtime context")
	}

	permalink, err := t.Slack.UploadFile(ctx, channel, threadTS, filename, imgData)
	if err != nil {
		return registry.Result{}, err
	}
	msg := "Screenshot uploaded to Slack"
	if permalink != "" {
		msg += ": " + permalink
	}
	return registry.Result{Content: msg}, nil
}

func parseDataURI(uri string) (data []byte, mimeType string, err error) {
	// Format: data:<mimeType>;base64,<data>
	if !strings.HasPrefix(uri, "data:") {
		return nil, "", fmt.Errorf("not a data URI")
	}
	rest := strings.TrimPrefix(uri, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return nil, "", fmt.Errorf("missing comma in data URI")
	}
	meta := rest[:comma]
	b64 := rest[comma+1:]

	parts := strings.Split(meta, ";")
	mimeType = parts[0]
	if len(parts) < 2 || parts[1] != "base64" {
		return nil, "", fmt.Errorf("only base64 data URIs are supported")
	}

	data, err = base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, "", fmt.Errorf("base64 decode: %w", err)
	}
	return data, mimeType, nil
}

func filenameForMIME(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return "screenshot.jpg"
	case "image/webp":
		return "screenshot.webp"
	default:
		return "screenshot.png"
	}
}
