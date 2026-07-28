package diagnostics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

func TestTimelineToolFormatsEvents(t *testing.T) {
	out, err := (TimelineTool{}).Execute(context.Background(), json.RawMessage(`{"events":[{"time":"10:00","source":"gcp-logs","event":"errors spiked","evidence":"500s"}]}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "10:00") || !strings.Contains(out.Content, "gcp-logs") {
		t.Fatalf("unexpected timeline: %q", out.Content)
	}
}

func TestEvidenceBoardToolFormatsSections(t *testing.T) {
	out, err := (EvidenceBoardTool{}).Execute(context.Background(), json.RawMessage(`{"facts":["log errors"],"hypotheses":["bad deploy"]}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "Verified facts") || !strings.Contains(out.Content, "bad deploy") {
		t.Fatalf("unexpected board: %q", out.Content)
	}
}
