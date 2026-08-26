package slacktool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agenttool "github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/client"
)

func TestFileSearchFindsTextExcerpt(t *testing.T) {
	client := fakeFileSearcher{
		file: slack.File{ID: "F123", Name: "app.log", Mimetype: "text/plain", Size: 200},
		data: []byte("boot ok\ncheckout failed with timeout\npayment ok\n"),
	}
	result, err := (FileSearchTool{Slack: client}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"file_id":"F123","query":"checkout timeout"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "checkout failed with timeout") || !strings.Contains(result.Text(), "F123") {
		t.Fatalf("unexpected content: %q", result.Text())
	}
}

func TestFileSearchWithoutQueryReturnsBeginning(t *testing.T) {
	client := fakeFileSearcher{
		file: slack.File{ID: "F123", Name: "runbook.md", Filetype: "md", Size: 200},
		data: []byte("# Runbook\nfirst step\nsecond step\n"),
	}
	result, err := (FileSearchTool{Slack: client}).Execute(context.Background(), agenttool.Call{Arguments: json.RawMessage(`{"file_id":"F123"}`), Scope: agenttool.Scope{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text(), "# Runbook") {
		t.Fatalf("unexpected content: %q", result.Text())
	}
}

type fakeFileSearcher struct {
	file slack.File
	data []byte
}

func (f fakeFileSearcher) FileInfo(ctx context.Context, fileID string) (slack.File, error) {
	return f.file, nil
}

func (f fakeFileSearcher) DownloadFile(ctx context.Context, file slack.File, maxBytes int64) ([]byte, error) {
	return f.data, nil
}
