package cloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBootstrapUsesHTTP1EvenWhenGatewayAdvertisesHTTP2(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("bootstrap negotiated %s", r.Proto)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing CLI authorization")
		}
		_, _ = w.Write([]byte(`{"model":"test-model","protocol":"kepler"}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = original }()
	info, err := FetchBootstrap(context.Background(), server.URL, "test-token")
	if err != nil || info.Model != "test-model" {
		t.Fatalf("bootstrap = %#v, %v", info, err)
	}
}
