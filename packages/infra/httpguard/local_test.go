package httpguard

import (
	"net/http/httptest"
	"testing"
)

func TestIsDirectLoopback(t *testing.T) {
	request := httptest.NewRequest("GET", "http://service/drain", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	if !IsDirectLoopback(request) {
		t.Fatal("direct loopback request was rejected")
	}
	request.Header.Set("X-Forwarded-For", "203.0.113.10")
	if IsDirectLoopback(request) {
		t.Fatal("proxied request was accepted as direct loopback")
	}
	request.Header.Del("X-Forwarded-For")
	request.RemoteAddr = "203.0.113.10:1234"
	if IsDirectLoopback(request) {
		t.Fatal("remote request was accepted")
	}
}
