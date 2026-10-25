package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
	"go.opentelemetry.io/otel/propagation"
)

// EmbeddingClient implements the documented OpenAI-compatible embeddings wire
// format. Configuration is explicit; it never guesses that a chat subscription
// or model also supports embeddings. One Embed call makes at most one request.
type EmbeddingClient struct {
	endpoint string
	token    string
	http     *http.Client
}

func NewEmbeddingClient(baseURL, token string, timeout time.Duration) (*EmbeddingClient, error) {
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || timeout <= 0 {
		return nil, fmt.Errorf("invalid embedding endpoint or timeout")
	}
	loopback := endpoint.Hostname() == "localhost"
	if ip := net.ParseIP(endpoint.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && loopback) {
		return nil, fmt.Errorf("embeddings require HTTPS or loopback HTTP")
	}
	return &EmbeddingClient{endpoint: strings.TrimRight(baseURL, "/") + "/embeddings", token: token, http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("embedding redirects are disabled") }}}, nil
}
func (c *EmbeddingClient) Embed(ctx context.Context, request model.EmbeddingRequest) (model.EmbeddingResponse, error) {
	if request.Model == "" || len(request.Input) == 0 || request.Dimensions < 0 || request.Dimensions > model.MaxEmbeddingDimensions {
		return model.EmbeddingResponse{}, &model.Error{Kind: model.ErrorInvalid, Message: "embedding model, nonempty input and nonnegative dimensions required"}
	}
	for _, text := range request.Input {
		if text == "" {
			return model.EmbeddingResponse{}, &model.Error{Kind: model.ErrorInvalid, Message: "embedding input cannot be empty"}
		}
	}
	body := map[string]any{"model": request.Model, "input": request.Input, "encoding_format": "float"}
	if request.Dimensions > 0 {
		body["dimensions"] = request.Dimensions
	}
	raw, _ := json.Marshal(body)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return model.EmbeddingResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.token)
	}
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(httpRequest.Header))
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return model.EmbeddingResponse{}, &model.Error{Kind: model.ErrorUnavailable, Message: "embedding transport failed", Cause: err}
	}
	defer response.Body.Close()
	// Never expose upstream bodies in errors: inputs can be private memories.
	if response.StatusCode != http.StatusOK {
		kind := model.ErrorUnavailable
		if response.StatusCode == 401 || response.StatusCode == 403 {
			kind = model.ErrorAuth
		} else if response.StatusCode == 429 {
			kind = model.ErrorRateLimited
		} else if response.StatusCode < 500 {
			kind = model.ErrorInvalid
		}
		return model.EmbeddingResponse{}, &model.Error{Kind: kind, StatusCode: response.StatusCode, Message: fmt.Sprintf("embedding provider HTTP %d", response.StatusCode)}
	}
	const maxResponseBytes = 8 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return model.EmbeddingResponse{}, &model.Error{Kind: model.ErrorProtocol, Message: "embedding response is incomplete or exceeds its byte budget"}
	}
	var parsed struct {
		Data []struct {
			Index  *int      `json:"index"`
			Vector []float64 `json:"embedding"`
		} `json:"data"`
		Usage *struct {
			Input *int64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	invalid := func(usage model.Usage) (model.EmbeddingResponse, error) {
		return model.EmbeddingResponse{Usage: usage}, &model.Error{Kind: model.ErrorProtocol, Message: "embedding response has an invalid shape or vector"}
	}
	if json.Unmarshal(data, &parsed) != nil {
		return invalid(model.Usage{})
	}
	var usage model.Usage
	if parsed.Usage != nil && parsed.Usage.Input != nil {
		if *parsed.Usage.Input < 0 {
			return invalid(usage)
		}
		usage = model.Usage{Reported: true, InputTokens: *parsed.Usage.Input}
	}
	if len(parsed.Data) != len(request.Input) {
		return invalid(usage)
	}
	vectors := make([][]float64, len(request.Input))
	dimensions := request.Dimensions
	for _, item := range parsed.Data {
		if item.Index == nil || *item.Index < 0 || *item.Index >= len(vectors) || vectors[*item.Index] != nil || len(item.Vector) == 0 || len(item.Vector) > model.MaxEmbeddingDimensions {
			return invalid(usage)
		}
		if dimensions == 0 {
			dimensions = len(item.Vector)
		}
		if len(item.Vector) != dimensions {
			return invalid(usage)
		}
		nonzero := false
		for _, value := range item.Vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return invalid(usage)
			}
			nonzero = nonzero || value != 0
		}
		if !nonzero {
			return invalid(usage)
		}
		vectors[*item.Index] = item.Vector
	}
	return model.EmbeddingResponse{Vectors: vectors, Usage: usage}, nil
}

var _ model.Embedder = (*EmbeddingClient)(nil)
