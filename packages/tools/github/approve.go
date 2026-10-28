package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/tool"
)

// ApprovePRTool submits only APPROVE for an explicitly selected immutable head.
// Hosted policy and the shared runtime require requester confirmation.
type ApprovePRTool struct{ Client Client }

var approvalHeadSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var approvalRepository = regexp.MustCompile(`^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$`)

func (t ApprovePRTool) Descriptor() tool.Descriptor {
	d := tool.FunctionDescriptor("github-approve_pr", "Request approval of a GitHub PR at one exact head SHA. Read github-pr_diff first and supply its full head_sha. Request this only after inspecting the full diff at that head and finding no obvious issues; report problems or uncertainty instead of approving. The requesting user must confirm in the shared Slack thread, and the credential owner is notified. Unavailable in DMs. This does not merge, comment, or request changes.", tool.ObjectSchema([]string{"url", "head_sha"}, map[string]any{
		"url":      map[string]any{"type": "string", "description": "GitHub pull request URL."},
		"head_sha": map[string]any{"type": "string", "pattern": "^[0-9a-fA-F]{40}$", "description": "Exact PR head commit being approved."},
	}), append(tool.ExternalWrite("github"), tool.WithParallel(false))...)
	digest := sha256.Sum256([]byte("approve-pr-v1\x00" + t.Client.baseURL() + "\x00" + t.Client.Token))
	d.Version = fmt.Sprintf("%x", digest)
	return d
}

func (t ApprovePRTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		URL     string `json:"url"`
		HeadSHA string `json:"head_sha"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	ref, err := parsePullURL(args.URL)
	if err != nil {
		return tool.Result{}, err
	}
	if !approvalRepository.MatchString(ref.repository) || !approvalHeadSHA.MatchString(args.HeadSHA) {
		return tool.Result{}, fmt.Errorf("a PR URL and full 40-character head_sha are required")
	}
	if !t.Client.enabled() {
		return tool.Result{}, fmt.Errorf("GitHub is not configured")
	}
	parts := strings.Split(ref.repository, "/")
	if parts[1] == "." || parts[1] == ".." {
		return tool.Result{}, fmt.Errorf("invalid GitHub repository")
	}
	path := "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + fmt.Sprintf("/pulls/%d", ref.number)
	var pr struct {
		State string `json:"state"`
		Head  struct {
			SHA string `json:"sha"`
		} `json:"head"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if _, err := t.Client.approvalJSON(ctx, http.MethodGet, path, nil, &pr); err != nil {
		return tool.Result{}, err
	}
	if pr.State != "open" {
		return tool.Result{}, fmt.Errorf("only an open PR can be approved")
	}
	if !strings.EqualFold(pr.Head.SHA, args.HeadSHA) {
		return tool.Result{}, fmt.Errorf("PR head changed; read the updated PR and request a new approval")
	}
	var actor struct {
		Login string `json:"login"`
	}
	if _, err := t.Client.approvalJSON(ctx, http.MethodGet, "/user", nil, &actor); err != nil {
		return tool.Result{}, err
	}
	if actor.Login == "" || pr.User.Login == "" {
		return tool.Result{}, fmt.Errorf("GitHub did not establish reviewer and author identities")
	}
	if strings.EqualFold(actor.Login, pr.User.Login) {
		return tool.Result{}, fmt.Errorf("GitHub does not allow approving your own pull request")
	}
	var review struct {
		ID       int64  `json:"id"`
		State    string `json:"state"`
		CommitID string `json:"commit_id"`
		HTMLURL  string `json:"html_url"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	uncertain, err := t.Client.approvalJSON(ctx, http.MethodPost, path+"/reviews", map[string]string{"event": "APPROVE", "commit_id": strings.ToLower(args.HeadSHA)}, &review)
	if uncertain {
		return unknownApprovalResult(), nil
	}
	if err != nil {
		return tool.Result{}, err
	}
	if review.ID <= 0 || review.State != "APPROVED" || !strings.EqualFold(review.CommitID, args.HeadSHA) || !strings.EqualFold(review.User.Login, actor.Login) || review.HTMLURL == "" {
		return unknownApprovalResult(), nil
	}
	return tool.TextResult(fmt.Sprintf("Approved %s#%d at %s as %s: %s", ref.repository, ref.number, review.CommitID, actor.Login, review.HTMLURL)), nil
}

func unknownApprovalResult() tool.Result {
	result := tool.TextResult("GitHub may have accepted this approval, but its receipt could not be verified. Do not repeat the write automatically; inspect the PR's reviews before requesting another approval.")
	result.IsError, result.ErrorCode = true, "external_outcome_unknown"
	return result
}

// approvalJSON performs one HTTP attempt. A failed write receipt is uncertain;
// it must never be replayed just because a transport or upstream failed.
func (c Client) approvalJSON(ctx context.Context, method, path string, input, output any) (bool, error) {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return false, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, body)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	write := method == http.MethodPost
	client := *c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return write, fmt.Errorf("GitHub approval request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return write && resp.StatusCode >= 500, fmt.Errorf("GitHub approval request returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return write, fmt.Errorf("GitHub approval response is incomplete or too large")
	}
	if err := json.Unmarshal(data, output); err != nil {
		return write, fmt.Errorf("GitHub approval response is invalid")
	}
	return false, nil
}
