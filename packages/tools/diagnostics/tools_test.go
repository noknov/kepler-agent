package diagnostics

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttool "github.com/noknov/kepler-agent/packages/agent/tool"
)

func TestTimelineToolFormatsEvents(t *testing.T) {
	out, err := (TimelineTool{}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"events":[{"time":"10:00","source":"gcp-logs","event":"errors spiked","evidence":"500s"}]}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text(), "10:00") || !strings.Contains(out.Text(), "gcp-logs") {
		t.Fatalf("unexpected timeline: %q", out.Text())
	}
}

func TestEvidenceBoardToolFormatsSections(t *testing.T) {
	out, err := (EvidenceBoardTool{}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"facts":["log errors"],"hypotheses":["bad deploy"]}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text(), "Verified facts") || !strings.Contains(out.Text(), "bad deploy") {
		t.Fatalf("unexpected board: %q", out.Text())
	}
}
