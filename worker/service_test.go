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

func TestRuntimeVersionDefaultsToV2WithExplicitV1Fallback(t *testing.T) {
	t.Setenv("AGENT_RUNTIME_VERSION", "")
	if got := runtimeVersion(); got != "v2" {
		t.Fatalf("default runtime = %q", got)
	}
	t.Setenv("AGENT_RUNTIME_VERSION", "v1")
	if got := runtimeVersion(); got != "v1" {
		t.Fatalf("fallback runtime = %q", got)
	}
	t.Setenv("AGENT_RUNTIME_VERSION", "unexpected")
	if got := runtimeVersion(); got != "v2" {
		t.Fatalf("unknown runtime = %q", got)
	}
}
