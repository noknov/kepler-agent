package cloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/config"
)

const (
	maxProxyRequestBytes   = 8 << 20
	defaultMaxOutputTokens = 65536
)

// JoinUpstreamURL maps a Kepler /v1 request onto an operator LLM base URL.
func JoinUpstreamURL(base, reqPath string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if reqPath == "" {
		reqPath = "/"
	}
	if !strings.HasPrefix(reqPath, "/") {
		reqPath = "/" + reqPath
	}
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(reqPath, "/v1/") {
		return base + strings.TrimPrefix(reqPath, "/v1")
	}
	if strings.HasSuffix(base, "/v1") && reqPath == "/v1" {
		return base
	}
	return base + reqPath
}

func NewSingleHostProxy(target *url.URL) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	original := proxy.Director
	proxy.Director = func(req *http.Request) {
		original(req)
		req.Host = target.Host
	}
	proxy.FlushInterval = 50 * time.Millisecond
	return proxy
}

func NewLLMUpstreamProxy(llm config.LLMConfig) (*httputil.ReverseProxy, error) {
	baseURL := strings.TrimSpace(llm.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("LLM base URL is required")
	}
	proxy := &httputil.ReverseProxy{
		FlushInterval: 50 * time.Millisecond,
		Rewrite: func(req *httputil.ProxyRequest) {
			joined, err := url.Parse(JoinUpstreamURL(baseURL, req.In.URL.Path))
			if err != nil {
				return
			}
			joined.RawQuery = req.In.URL.RawQuery
			req.SetURL(joined)
			req.Out.Host = joined.Host
			applyUpstreamAuth(req.Out.Header, llm)
		},
	}
	return proxy, nil
}

// RestrictLLMProxy limits the authenticated compatibility endpoints to the
// operator-selected model and output budget before the API key is attached.
func RestrictLLMProxy(next http.Handler, llm config.LLMConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyRequestBytes+1))
		if err != nil || len(body) > maxProxyRequestBytes {
			http.Error(w, "request body is too large", http.StatusRequestEntityTooLarge)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		configuredModel := strings.TrimSpace(llm.Model)
		requestedModel, _ := payload["model"].(string)
		if configuredModel == "" || (requestedModel != "" && requestedModel != configuredModel) {
			http.Error(w, "model is not allowed", http.StatusForbidden)
			return
		}
		payload["model"] = configuredModel
		limit := llm.MaxOutputTokens
		if limit <= 0 {
			limit = defaultMaxOutputTokens
		}
		field := "max_tokens"
		if r.URL.Path == "/v1/responses" {
			field = "max_output_tokens"
		}
		if current, ok := numericInt(payload[field]); !ok || current <= 0 || current > limit {
			payload[field] = limit
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(encoded))
		r.ContentLength = int64(len(encoded))
		next.ServeHTTP(w, r)
	})
}

func numericInt(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}

func limitConcurrency(next http.Handler, maximum int) http.Handler {
	if maximum < 1 {
		maximum = 1
	}
	semaphore := make(chan struct{}, maximum)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case semaphore <- struct{}{}:
			defer func() { <-semaphore }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many concurrent model requests", http.StatusTooManyRequests)
		}
	})
}

func applyUpstreamAuth(header http.Header, llm config.LLMConfig) {
	key := strings.TrimSpace(llm.APIKey)
	if key == "" {
		return
	}
	if strings.EqualFold(llm.Protocol, "anthropic") {
		header.Del("Authorization")
		header.Del("X-Api-Key")
		if strings.HasPrefix(strings.ToLower(key), "bearer ") {
			header.Set("Authorization", key)
			if !strings.EqualFold(llm.AnthropicFlavor, "claude-code") {
				header.Set("x-api-key", strings.TrimSpace(key[7:]))
			}
			return
		}
		header.Set("x-api-key", key)
		if strings.EqualFold(llm.AnthropicFlavor, "claude-code") {
			header.Set("Authorization", "Bearer "+key)
			header.Set("x-app", "cli")
		}
		return
	}
	if strings.HasPrefix(strings.ToLower(key), "bearer ") {
		header.Set("Authorization", key)
		return
	}
	header.Set("Authorization", "Bearer "+key)
}
