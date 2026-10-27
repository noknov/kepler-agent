package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestTaskSnapshotReadsAuthorizedCanonicalRevision(t *testing.T) {
	client := testClient("", "", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/org/project/pulls/42" || r.Header.Get("Authorization") != "Bearer ghp-test" {
			t.Fatal(r.URL, r.Header)
		}
		return response(200, `{"node_id":"PR-node","number":42,"title":"Actual title","state":"closed","merged":true,"updated_at":"2026-10-28T01:00:00Z","head":{"sha":"immutable-head"}}`), nil
	}))
	snapshot, err := client.ReadTaskSnapshot(t.Context(), "https://github.com/org/project/pull/42?tracking=1#discussion")
	if err != nil || snapshot.ID != "PR-node" || snapshot.URL != "https://github.com/org/project/pull/42" || snapshot.State != "merged" || snapshot.HeadSHA != "immutable-head" || snapshot.Kind != "github_pull_request" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestTaskSnapshotRejectsUntrustedTargetsAndIncompleteData(t *testing.T) {
	calls := 0
	client := testClient("", "", roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"number":42,"title":"missing identity"}`), nil
	}))
	for _, target := range []string{"https://evil.test/org/repo/issues/42", "http://github.com/org/repo/issues/42", "https://token@github.com/org/repo/issues/42", "https://github.com/org/repo/issues/42/comments", "https://github.com/../repo/issues/42"} {
		if _, err := client.ReadTaskSnapshot(context.Background(), target); err == nil {
			t.Fatal("accepted", target)
		}
	}
	if calls != 0 {
		t.Fatalf("untrusted target reached API: %d", calls)
	}
	if _, err := client.ReadTaskSnapshot(t.Context(), "https://github.com/org/project/issues/42"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete API response became a snapshot: %v", err)
	}
	client.Token = ""
	if _, err := client.ReadTaskSnapshot(t.Context(), "https://github.com/org/project/issues/42"); err == nil || calls != 1 {
		t.Fatalf("missing user token fell back to anonymous API: %v calls=%d", err, calls)
	}
}
