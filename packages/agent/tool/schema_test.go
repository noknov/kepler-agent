package tool

import (
	"encoding/json"
	"testing"
)

func TestObjectSchemaSerializesEmptyPropertiesAsObject(t *testing.T) {
	data, err := json.Marshal(ObjectSchema(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Properties == nil {
		t.Fatalf("properties = null, want empty object: %s", data)
	}
}

func TestFunctionDescriptorDefaultsReadToolsToParallel(t *testing.T) {
	read := FunctionDescriptor("code-read_file", "read", ObjectSchema(nil, map[string]any{"path": map[string]any{"type": "string"}}))
	if !read.Parallel {
		t.Fatal("read tools must default to parallel")
	}
	write := FunctionDescriptor("code-write_file", "write", ObjectSchema(nil, nil), WithParallel(false))
	if write.Parallel {
		t.Fatal("explicit sequential tools must stay sequential")
	}
	network := FunctionDescriptor("github-workflow_runs", "runs", ObjectSchema(nil, nil), NetworkIntegration("github")...)
	if !network.Parallel {
		t.Fatal("network-only tools must default to parallel")
	}
	mutating := FunctionDescriptor("tts-speak", "speak", ObjectSchema(nil, nil), ExternalWrite()...)
	if mutating.Parallel {
		t.Fatal("external writes must stay sequential")
	}
}
