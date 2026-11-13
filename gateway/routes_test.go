package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/noknov/kepler-agent/packages/platform"
)

func TestOnlySlackAndAuthenticatedBenchmarkRoutesAreExposed(t *testing.T) {
	service := &Service{stores: &platform.EventIngressStores{}}
	routes, err := service.routes()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/auth/slack/callback", "/api/conversations", "/cli/bootstrap", "/cli/login"} {
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 404 {
			t.Errorf("retired route %s returned %d", path, response.Code)
		}
	}
	for _, path := range []string{"/v1/responses", "/v1/chat/completions", "/v1/messages"} {
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, httptest.NewRequest("POST", path, nil))
		if response.Code != 404 {
			t.Errorf("retired proxy %s returned %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest("GET", "/benchmark/bootstrap", nil))
	if response.Code != 401 {
		t.Fatalf("benchmark access bypassed authentication: %d", response.Code)
	}
}
