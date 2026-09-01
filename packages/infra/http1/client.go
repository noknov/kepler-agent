package http1

import (
	"crypto/tls"
	"net/http"
	"time"
)

// Client talks HTTP/1.1 only. Ngrok free tunnels often advertise HTTP/2 via
// ALPN then fail with "http2: client conn could not be established" on
// long-lived POSTs such as Kepler generate streams.
func Client(timeout time.Duration) *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Client{Timeout: timeout}
	}
	cloned := transport.Clone()
	cloned.ForceAttemptHTTP2 = false
	// Disable the http2 transport hook and pin ALPN to http/1.1. An empty
	// TLSNextProto map alone is not enough: without an explicit NextProtos the
	// ngrok edge may still speak HTTP/2, which breaks the HTTP/1.x reader.
	cloned.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	tlsConfig := cloned.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		cp := tlsConfig.Clone()
		tlsConfig = cp
	}
	tlsConfig.NextProtos = []string{"http/1.1"}
	cloned.TLSClientConfig = tlsConfig
	cloned.DisableCompression = true
	return &http.Client{Timeout: timeout, Transport: cloned}
}

// Standard is the default client for short requests (bootstrap, login). Ngrok
// works reliably with HTTP/2 on these; forcing HTTP/1.1 can break ALPN.
func Standard(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

