package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type CompactClient interface {
	Compact(context.Context, Request) (Response, error)
}

// Compact preserves the complete returned window. A compaction item alone is
// insufficient: retained messages and unknown provider items must round-trip.
func (c *OpenAIResponsesClient) Compact(ctx context.Context, req Request) (Response, error) {
	payload, err := json.Marshal(map[string]any{"model": req.Model, "input": responsesInput(req.Messages)})
	if err != nil {
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses/compact", bytes.NewReader(payload))
	if err != nil {
		return Response{}, err
	}
	c.setHeaders(request, req.Metadata)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()
	data, err := readJSONResponse(response, c.providerName()+" compact")
	if err != nil {
		return Response{}, err
	}
	var parsed struct {
		Output []json.RawMessage `json:"output"`
		Usage  responsesUsage    `json:"usage"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return Response{}, err
	}
	found := false
	for _, raw := range parsed.Output {
		var item struct {
			Type      string `json:"type"`
			Encrypted string `json:"encrypted_content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return Response{}, fmt.Errorf("invalid compacted item")
		}
		if item.Type == "compaction" && item.Encrypted != "" {
			found = true
		}
	}
	if !found {
		return Response{}, fmt.Errorf("native compaction returned no encrypted compaction item")
	}
	return Response{Message: Message{Role: "user", NativeItems: parsed.Output}, Usage: parsed.Usage.toUsage(), FinishReason: "completed"}, nil
}
