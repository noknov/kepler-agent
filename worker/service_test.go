package worker

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDrainIsLocalOnly(t *testing.T) {
	service := &Service{}
	remote := httptest.NewRequest(http.MethodPost, "http://worker/drain", nil)
	remote.RemoteAddr = "203.0.113.10:1234"
	remoteRecorder := httptest.NewRecorder()
	service.handleDrain(remoteRecorder, remote)
	if remoteRecorder.Code != http.StatusForbidden || service.draining.Load() {
		t.Fatalf("remote drain code=%d draining=%v", remoteRecorder.Code, service.draining.Load())
	}

	local := httptest.NewRequest(http.MethodPost, "http://worker/drain", nil)
	local.RemoteAddr = "127.0.0.1:1234"
	localRecorder := httptest.NewRecorder()
	service.handleDrain(localRecorder, local)
	if localRecorder.Code != http.StatusOK || !service.draining.Load() {
		t.Fatalf("local drain code=%d draining=%v", localRecorder.Code, service.draining.Load())
	}
}
