package slackhome

import (
	"context"
	"encoding/json"
	"github.com/noknov/kepler-agent/packages/memory"
	"strings"
	"testing"
)

type resetMemoryStub struct{}

func (resetMemoryStub) Reset(context.Context, memory.Identity) error { return nil }
func TestHomeMemoryResetIsConfirmedAndHasNoOwnerPayload(t *testing.T) {
	view := Controller{Memory: resetMemoryStub{}}.View("U1")
	data, _ := json.Marshal(view)
	body := string(data)
	if !strings.Contains(body, "reset_user_memory") || !strings.Contains(body, "Clear Memories") || !strings.Contains(body, "Clear Memories?") || strings.Contains(body, `"value":"U1"`) {
		t.Fatalf("invalid memory control: %s", body)
	}
}
