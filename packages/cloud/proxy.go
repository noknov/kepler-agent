package cloud

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

const (
	maxProxyRequestBytes   = 8 << 20
	defaultMaxOutputTokens = 65536
)

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
