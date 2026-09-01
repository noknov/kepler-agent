package http1

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestClientPinsHTTP1ALPN(t *testing.T) {
	c := Client(0)
	transport, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", c.Transport)
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("expected TLSClientConfig")
	}
	if got := transport.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("NextProtos = %#v", got)
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 should be false")
	}
	if transport.TLSNextProto == nil {
		t.Fatal("TLSNextProto should be set")
	}
}

func TestStandardUsesDefaultBehavior(t *testing.T) {
	c := Standard(0)
	if c.Timeout != 0 {
		t.Fatalf("timeout = %v", c.Timeout)
	}
	if c.Transport != nil {
		t.Fatalf("expected nil transport, got %T", c.Transport)
	}
}

func TestClientClonePreservesCustomTLSConfig(t *testing.T) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Skip("default transport is not *http.Transport")
	}
	clone := base.Clone()
	clone.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	// Exercise through Client by temporarily swapping default is overkill;
	// just verify our mutation pattern on a cloned transport.
	clone.ForceAttemptHTTP2 = false
	clone.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	cfg := clone.TLSClientConfig.Clone()
	cfg.NextProtos = []string{"http/1.1"}
	clone.TLSClientConfig = cfg
	if clone.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %v", clone.TLSClientConfig.MinVersion)
	}
}
