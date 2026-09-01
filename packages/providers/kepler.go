package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"github.com/noknov/kepler-agent/packages/infra/http1"
)

type keplerRemote struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func newKeplerRemote(baseURL, apiKey string, timeout time.Duration) keplerRemote {
	return keplerRemote{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		apiKey:  strings.TrimSpace(apiKey),
		httpClient: http1.Client(timeout),
	}
}

func (c keplerRemote) Generate(ctx context.Context, request model.Request, sink model.EventSink) (model.Response, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return model.Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+KeplerGeneratePath, bytes.NewReader(payload))
	if err != nil {
		return model.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")
	httpReq.Header.Set("ngrok-skip-browser-warning", "true")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return model.Response{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return model.Response{}, &model.Error{
			Kind:       model.ErrorUnavailable,
			Message:    fmt.Sprintf("kepler generate: %s: %s", resp.Status, strings.TrimSpace(string(body))),
			Retryable:  resp.StatusCode >= 500,
			StatusCode: resp.StatusCode,
		}
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var result *model.Response
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var frame HostedStreamLine
		if err := json.Unmarshal(line, &frame); err != nil {
			return model.Response{}, fmt.Errorf("kepler generate: decode stream: %w", err)
		}
		switch frame.Kind {
		case "event":
			if sink != nil && frame.Event != nil {
				if err := sink(*frame.Event); err != nil {
					return model.Response{}, err
				}
			}
		case "result":
			if frame.Response == nil {
				return model.Response{}, fmt.Errorf("kepler generate: empty result")
			}
			copied := *frame.Response
			result = &copied
		case "error":
			if frame.Error != nil {
				return model.Response{}, frame.Error
			}
			return model.Response{}, fmt.Errorf("kepler generate failed")
		default:
			return model.Response{}, fmt.Errorf("kepler generate: unknown frame %q", frame.Kind)
		}
	}
	if err := scanner.Err(); err != nil {
		return model.Response{}, err
	}
	if result == nil {
		return model.Response{}, fmt.Errorf("kepler generate: stream ended without a result")
	}
	return *result, nil
}

type HostedStreamLine struct {
	Kind     string             `json:"kind"`
	Event    *model.StreamEvent `json:"event,omitempty"`
	Response *model.Response    `json:"response,omitempty"`
	Error    *model.Error       `json:"error,omitempty"`
}
