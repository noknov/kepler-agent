package slacktool

import (
	"context"
	"testing"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

type stubTool struct {
	name string
	opts []tool.DescriptorOption
}

func (s stubTool) Descriptor() tool.Descriptor {
	return tool.FunctionDescriptor(s.name, "", tool.ObjectSchema(nil, map[string]any{}), s.opts...)
}

func (stubTool) Execute(context.Context, tool.Call) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

func TestBindSurfaceAddsSlackMetadata(t *testing.T) {
	bound := bindSurface(stubTool{name: "reminder-create", opts: tool.ExternalWrite()}, "reminder").Descriptor()
	if len(bound.Surfaces) != 1 || bound.Surfaces[0] != "slack" {
		t.Fatalf("surfaces=%v", bound.Surfaces)
	}
	if len(bound.Dependencies) != 2 || bound.Dependencies[0] != "slack" || bound.Dependencies[1] != "reminder" {
		t.Fatalf("dependencies=%v", bound.Dependencies)
	}
	if len(bound.Effects) != 2 {
		t.Fatalf("effects=%v", bound.Effects)
	}
}

func TestReadNetworkKeepsSlackInSurfacePackage(t *testing.T) {
	desc := stubTool{name: "slack-file_search", opts: readNetwork()}.Descriptor()
	if desc.Surfaces[0] != "slack" {
		t.Fatalf("surfaces=%v", desc.Surfaces)
	}
	if !desc.Parallel {
		t.Fatal("expected parallel read tool")
	}
}
