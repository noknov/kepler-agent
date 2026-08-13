package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
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

type descriptionTool struct {
	descriptor tool.Descriptor
}

func (t descriptionTool) Descriptor() tool.Descriptor { return t.descriptor }
func (descriptionTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, nil
}

func TestToolDescriptionsUsesCatalogDescriptors(t *testing.T) {
	catalog, err := tool.NewCatalog(
		descriptionTool{descriptor: tool.Descriptor{Name: "github-pr_diff", Description: "Fetch GitHub pull request metadata and unified diff.", InputSchema: json.RawMessage(`{}`), Effects: []tool.Effect{tool.EffectRead}}},
		descriptionTool{descriptor: tool.Descriptor{Name: "empty-description", InputSchema: json.RawMessage(`{}`), Effects: []tool.Effect{tool.EffectRead}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	descriptions := toolDescriptions(catalog)
	if got := descriptions["github-pr_diff"]; got != "Fetch GitHub pull request metadata and unified diff." {
		t.Fatalf("github-pr_diff description = %q", got)
	}
	if _, ok := descriptions["empty-description"]; ok {
		t.Fatalf("empty description should be omitted: %#v", descriptions)
	}
}
