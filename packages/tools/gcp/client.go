package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/connections"
)

// Defaults holds deployment-wide GCP defaults.
type Defaults struct {
	Project   string
	Namespace string
	Cluster   string
	Region    string
}

// Client performs read-only GCP API calls with a user OAuth access token.
type Client struct {
	AccessToken string
	Defaults    Defaults
	HTTP        *http.Client
}

func (c Client) enabled() bool {
	return strings.TrimSpace(c.AccessToken) != ""
}

func (c Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// TokenSource resolves a GCP API client for a tool call.
type TokenSource interface {
	Resolve(ctx context.Context, call tool.Call) (Client, error)
}

// ConnectedSource uses per-user GCP OAuth tokens.
type ConnectedSource struct {
	Service  connections.Service
	Defaults Defaults
}

func (s ConnectedSource) Resolve(ctx context.Context, call tool.Call) (Client, error) {
	if s.Service.Store == nil {
		return Client{}, fmt.Errorf("gcp connections are not configured")
	}
	if call.Scope.UserID == "" {
		return Client{}, fmt.Errorf("user id is required for gcp")
	}
	token, err := s.Service.GCPAccessToken(ctx, call.Scope.UserID)
	if err != nil {
		if errorsIsNotConnected(err) {
			return Client{}, s.Service.Required(call.Scope.UserID, connections.ProviderGCP)
		}
		return Client{}, err
	}
	return Client{AccessToken: token, Defaults: s.Defaults}, nil
}

func errorsIsNotConnected(err error) bool {
	return err == connections.ErrNotConnected || strings.Contains(err.Error(), "not connected")
}

func begin(ctx context.Context, source TokenSource, call tool.Call) (Client, *tool.Result, error) {
	if source == nil {
		return Client{}, nil, fmt.Errorf("gcp is not configured: connect Google Cloud in App Home")
	}
	client, err := source.Resolve(ctx, call)
	if err != nil {
		if result, convErr := toolResult(err); convErr == nil {
			return Client{}, &result, nil
		}
		return Client{}, nil, err
	}
	if !client.enabled() {
		return Client{}, nil, fmt.Errorf("gcp access token is empty")
	}
	return client, nil, nil
}

func toolResult(err error) (tool.Result, error) {
	if err == nil {
		return tool.Result{}, nil
	}
	if result, convErr := connections.ToolResult(err); convErr == nil {
		return result, nil
	}
	return tool.Result{}, err
}

func (c Client) doJSON(ctx context.Context, method, rawURL string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(data))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("gcp api %s %s failed: %s", method, rawURL, msg)
	}
	return data, nil
}

func (c Client) projectID(project string) (string, error) {
	project = strings.TrimSpace(project)
	if project == "" {
		project = strings.TrimSpace(c.Defaults.Project)
	}
	if project == "" {
		return "", fmt.Errorf("GCP project is required; pass project in tool args or configure GCP_PROJECT as a default")
	}
	return project, nil
}

func (c Client) region(region string) string {
	region = strings.TrimSpace(region)
	if region == "" {
		region = strings.TrimSpace(c.Defaults.Region)
	}
	return region
}

func (c Client) listLogEntries(ctx context.Context, project, filter string, limit int) ([]byte, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	body := map[string]any{
		"resourceNames": []string{"projects/" + project},
		"filter":        filter,
		"orderBy":       "timestamp desc",
		"pageSize":      limit,
	}
	return c.doJSON(ctx, http.MethodPost, "https://logging.googleapis.com/v2/entries:list", body)
}

func (c Client) listRunServices(ctx context.Context, project, region string) ([]byte, error) {
	if region == "" {
		return nil, fmt.Errorf("region is required; pass region in tool args or configure GKE_REGION as a default")
	}
	rawURL := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/services", project, region)
	return c.doJSON(ctx, http.MethodGet, rawURL, nil)
}

func (c Client) describeRunService(ctx context.Context, project, region, service string) ([]byte, error) {
	if region == "" {
		return nil, fmt.Errorf("region is required; pass region in tool args or configure GKE_REGION as a default")
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, fmt.Errorf("service name is required for action=describe")
	}
	rawURL := fmt.Sprintf("https://run.googleapis.com/v2/projects/%s/locations/%s/services/%s", project, region, url.PathEscape(service))
	return c.doJSON(ctx, http.MethodGet, rawURL, nil)
}

func (c Client) listRunRevisions(ctx context.Context, project, region, service string, limit int) ([]byte, error) {
	if region == "" {
		return nil, fmt.Errorf("region is required; pass region in tool args or configure GKE_REGION as a default")
	}
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, fmt.Errorf("service name is required to list revisions")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rawURL := fmt.Sprintf(
		"https://run.googleapis.com/v2/projects/%s/locations/%s/services/%s/revisions?pageSize=%d",
		project, region, url.PathEscape(service), limit,
	)
	return c.doJSON(ctx, http.MethodGet, rawURL, nil)
}

func (c Client) listClusters(ctx context.Context, project string) ([]byte, error) {
	rawURL := fmt.Sprintf("https://container.googleapis.com/v1/projects/%s/locations/-/clusters", project)
	return c.doJSON(ctx, http.MethodGet, rawURL, nil)
}

func (c Client) describeCluster(ctx context.Context, project, location, cluster string) ([]byte, error) {
	cluster = strings.TrimSpace(cluster)
	if cluster == "" {
		return nil, fmt.Errorf("cluster name is required for action=describe")
	}
	location = strings.TrimSpace(location)
	if location == "" {
		location = c.region("")
	}
	if location == "" {
		return nil, fmt.Errorf("region or zone is required for action=describe")
	}
	rawURL := fmt.Sprintf(
		"https://container.googleapis.com/v1/projects/%s/locations/%s/clusters/%s",
		project, location, url.PathEscape(cluster),
	)
	return c.doJSON(ctx, http.MethodGet, rawURL, nil)
}
