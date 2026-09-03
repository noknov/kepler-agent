package gateway

import (
	"context"
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
