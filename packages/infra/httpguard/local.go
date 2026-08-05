package httpguard

import (
	"net"
	"net/http"
	"strings"
)

// IsDirectLoopback accepts only a direct TCP loopback peer. Proxy headers are
// rejected even when RemoteAddr is loopback because an ingress proxy must not
// gain access to local-only administrative endpoints.
func IsDirectLoopback(r *http.Request) bool {
	if r == nil || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-IP") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(r.RemoteAddr)
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
