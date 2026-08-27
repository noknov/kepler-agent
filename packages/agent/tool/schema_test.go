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
