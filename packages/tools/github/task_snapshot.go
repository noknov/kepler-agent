package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TaskSnapshot is authoritative issue/PR metadata, without model-authored state.
type TaskSnapshot struct {
	ID, Kind, URL, Title, State, Repository, HeadSHA string
	Number                                           int64
	UpdatedAt                                        time.Time
}

// ReadTaskSnapshot resolves a public GitHub URL through the configured API and
// caller token. The URL never selects the API origin or supplies credentials.
func (c Client) ReadTaskSnapshot(ctx context.Context, raw string) (TaskSnapshot, error) {
	if !c.enabled() {
		return TaskSnapshot{}, fmt.Errorf("GitHub task lookup requires the connected user token")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil {
		return TaskSnapshot{}, fmt.Errorf("task URL must be an HTTPS github.com issue or PR URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || (parts[2] != "issues" && parts[2] != "pull") {
		return TaskSnapshot{}, fmt.Errorf("task URL must identify one GitHub issue or pull request")
	}
	for _, part := range parts[:2] {
		if part == "." || part == ".." {
			return TaskSnapshot{}, fmt.Errorf("invalid repository path")
		}
	}
	number, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || number <= 0 {
		return TaskSnapshot{}, fmt.Errorf("task number must be positive")
	}
	repository := parts[0] + "/" + parts[1]
	kind, endpoint := "github_issue", "issues"
	if parts[2] == "pull" {
		kind, endpoint = "github_pull_request", "pulls"
	}
	api := fmt.Sprintf("%s/repos/%s/%s/%s/%d", c.baseURL(), url.PathEscape(parts[0]), url.PathEscape(parts[1]), endpoint, number)
	data, err := c.do(ctx, http.MethodGet, api, nil)
	if err != nil {
		return TaskSnapshot{}, err
	}
	var out struct {
		ID        string    `json:"node_id"`
		Title     string    `json:"title"`
		State     string    `json:"state"`
		UpdatedAt time.Time `json:"updated_at"`
		Number    int64     `json:"number"`
		Merged    bool      `json:"merged"`
		Head      struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return TaskSnapshot{}, fmt.Errorf("decode GitHub task metadata: %w", err)
	}
	if out.ID == "" || out.Title == "" || out.State == "" || out.Number != number || out.UpdatedAt.IsZero() || (endpoint == "pulls" && out.Head.SHA == "") {
		return TaskSnapshot{}, fmt.Errorf("GitHub task metadata is incomplete")
	}
	state := out.State
	if out.Merged {
		state = "merged"
	}
	canonical := fmt.Sprintf("https://github.com/%s/%s/%d", repository, parts[2], number)
	return TaskSnapshot{ID: out.ID, Kind: kind, URL: canonical, Title: out.Title, State: state, Repository: repository, Number: number, HeadSHA: out.Head.SHA, UpdatedAt: out.UpdatedAt}, nil
}
