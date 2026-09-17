package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/noknov/kepler-agent/packages/mcp"
)

func TestDiscoverForRegistrationCredentialRejected(t *testing.T) {
	client := &mcp.Client{
		ServiceName: "demo",
		URL:         "https://mcp.test",
		Token:       "bad",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return response(http.StatusUnauthorized, `{"error":"Please authenticate"}`), nil
		})},
	}
	_, err := DiscoverForRegistration(context.Background(), Server{Name: "demo", Client: client})
	if !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("err=%v want ErrCredentialRejected", err)
	}
}

func TestDiscoverDeferredToolsClearsConnection(t *testing.T) {
	client := &mcp.Client{
		ServiceName: "demo",
		URL:         "https://mcp.test",
		Token:       "bad",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return response(http.StatusUnauthorized, `{"error":"Please authenticate"}`), nil
		})},
	}
	cleared := false
	items, err := DiscoverDeferredTools(context.Background(), "demo", "U1", Server{Name: "demo", Client: client}, func(ctx context.Context, userID string) error {
		if userID != "U1" {
			t.Fatalf("userID=%q", userID)
		}
		cleared = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if items != nil {
		t.Fatalf("items=%v", items)
	}
	if !cleared {
		t.Fatal("expected connection clear")
	}
}

func TestDiscoverDeferredToolsPropagatesOtherErrors(t *testing.T) {
	client := &mcp.Client{
		ServiceName: "demo",
		URL:         "https://mcp.test",
		HTTP: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var payload struct {
				Method string `json:"method"`
			}
			_ = json.NewDecoder(request.Body).Decode(&payload)
			if payload.Method == "notifications/initialized" {
				return response(http.StatusAccepted, ""), nil
			}
			return response(http.StatusBadGateway, "down"), nil
		})},
	}
	_, err := DiscoverDeferredTools(context.Background(), "demo", "U1", Server{Name: "demo", Client: client}, nil)
	if err == nil || errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("err=%v", err)
	}
}
