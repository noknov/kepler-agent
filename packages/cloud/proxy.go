package cloud

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/config"
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
