package slacktool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttool "github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
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
	result, err := (JSONAnalyzeTool{Slack: client}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"file_id":"FJSON","group_by":"region","metrics":["amount"],"top_fields":["status"]}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"records: 3", "amount: count=3", "status:", "sg: count=2", "amount_sum=35"} {
		if !strings.Contains(result.Text(), want) {
			t.Fatalf("missing %q in:\n%s", want, result.Text())
		}
	}
}

func TestJSONAnalyzeSupportsJSONLines(t *testing.T) {
	client := fakeFileSearcher{
		file: slack.File{ID: "FJSONL", Name: "events.jsonl", Filetype: "json", Size: 200},
		data: []byte("{\"type\":\"a\",\"count\":1}\n{\"type\":\"a\",\"count\":3}\n{\"type\":\"b\",\"count\":2}\n"),
	}
	result, err := (JSONAnalyzeTool{Slack: client}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"file_id":"FJSONL","group_by":"type","metrics":["count"]}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "shape: jsonl") || !strings.Contains(result.Text(), "a: count=2") {
		t.Fatalf("unexpected content:\n%s", result.Text())
	}
}
