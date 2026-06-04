package slacktool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wati/oncall-agent/internal/slack"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

func TestJSONAnalyzeSummarizesArray(t *testing.T) {
	client := fakeFileSearcher{
		file: slack.File{ID: "FJSON", Name: "stats.json", Mimetype: "application/json", Size: 200},
		data: []byte(`[
			{"region":"sg","status":"ok","amount":10},
			{"region":"sg","status":"fail","amount":25},
			{"region":"hk","status":"ok","amount":5}
		]`),
	}
	result, err := (JSONAnalyzeTool{Slack: client}).Execute(context.Background(), json.RawMessage(`{"file_id":"FJSON","group_by":"region","metrics":["amount"],"top_fields":["status"]}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"records: 3", "amount: count=3", "status:", "sg: count=2", "amount_sum=35"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("missing %q in:\n%s", want, result.Content)
		}
	}
}

func TestJSONAnalyzeSupportsJSONLines(t *testing.T) {
	client := fakeFileSearcher{
		file: slack.File{ID: "FJSONL", Name: "events.jsonl", Filetype: "json", Size: 200},
		data: []byte("{\"type\":\"a\",\"count\":1}\n{\"type\":\"a\",\"count\":3}\n{\"type\":\"b\",\"count\":2}\n"),
	}
	result, err := (JSONAnalyzeTool{Slack: client}).Execute(context.Background(), json.RawMessage(`{"file_id":"FJSONL","group_by":"type","metrics":["count"]}`), registry.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "shape: jsonl") || !strings.Contains(result.Content, "a: count=2") {
		t.Fatalf("unexpected content:\n%s", result.Content)
	}
}
