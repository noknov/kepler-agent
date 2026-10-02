package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCancelWebRequestsDoesNotRequireGatewayShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{webCancels: map[uint64]context.CancelFunc{1: cancel}}
	service.cancelWebRequests()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("web request context was not cancelled")
	}
}

func TestWebProxyDoesNotExposeWorkerDiagnostics(t *testing.T) {
	forwarded := 0
	s := &Service{webCancels: map[uint64]context.CancelFunc{}, webProxy: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { forwarded++; w.WriteHeader(http.StatusOK) })}
	for _, tc := range []struct {
		path   string
		status int
	}{{"/metrics", http.StatusNotFound}, {"/", http.StatusOK}, {"/api/bootstrap", http.StatusOK}} {
		w := httptest.NewRecorder()
		s.serveWeb(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("path %s: got %d", tc.path, w.Code)
		}
	}
	if forwarded != 2 {
		t.Fatal("diagnostics reached worker through Web proxy")
	}
}
