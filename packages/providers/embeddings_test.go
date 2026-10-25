package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

func TestEmbeddingResponseShapeAndAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		valid, known bool
	}{
		{"reordered", `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":5}}`, true, true},
		{"unknown usage", `{"data":[{"index":0,"embedding":[1,0]},{"index":1,"embedding":[0,1]}],"usage":{}}`, true, false},
		{"duplicate index", `{"data":[{"index":0,"embedding":[1,0]},{"index":0,"embedding":[0,1]}],"usage":{"prompt_tokens":5}}`, false, true},
		{"missing index", `{"data":[{"embedding":[1,0]},{"index":1,"embedding":[0,1]}],"usage":{"prompt_tokens":5}}`, false, true},
		{"wrong dimensions", `{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[0,1]}],"usage":{"prompt_tokens":5}}`, false, true},
		{"zero vector", `{"data":[{"index":0,"embedding":[0,0]},{"index":1,"embedding":[0,1]}],"usage":{"prompt_tokens":5}}`, false, true},
		{"missing vector", `{"data":[{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":5}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var request map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&request) != nil || r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer private-token" || string(request["encoding_format"]) != `"float"` {
					t.Error("invalid embedding wire request")
				}
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := NewEmbeddingClient(server.URL+"/v1", "private-token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Embed(context.Background(), model.EmbeddingRequest{Model: "selected-model", Input: []string{"one", "two"}, Dimensions: 2})
			if (err == nil) != tc.valid || result.Usage.Known() != tc.known || calls != 1 {
				t.Fatalf("result=%#v calls=%d err=%v", result, calls, err)
			}
			if tc.valid && result.Vectors[0][0] != 1 {
				t.Fatal("provider index order was not preserved")
			}
		})
	}
}

func TestEmbeddingErrorsNeverExposeBodiesOrFollowRedirects(t *testing.T) {
	escaped := 0
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { escaped++ }))
	defer destination.Close()
	for _, status := range []int{401, 429, 500, 307} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == 307 {
					http.Redirect(w, r, destination.URL, status)
				} else {
					w.WriteHeader(status)
					io.WriteString(w, "PRIVATE_MEMORY_CONTENT")
				}
			}))
			defer server.Close()
			client, err := NewEmbeddingClient(server.URL, "private-token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Embed(context.Background(), model.EmbeddingRequest{Model: "selected", Input: []string{"private input"}})
			if err == nil || strings.Contains(err.Error(), "PRIVATE_") || strings.Contains(err.Error(), "private-token") || escaped != 0 {
				t.Fatalf("error=%v escaped=%d", err, escaped)
			}
		})
	}
}
