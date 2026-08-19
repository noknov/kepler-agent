package k8s

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/tool"
	"github.com/noknov/slack-copilot-agent/packages/connections"
)

// Client performs read-only Kubernetes API calls on GKE using a user OAuth token.
type Client struct {
	AccessToken string
	Defaults    Defaults
	HTTP        *http.Client
	mu          sync.Mutex
	clusters    map[string]*clusterSession
}

type clusterSession struct {
	baseURL string
	client  *http.Client
}

// TokenSource resolves a Kubernetes API client for a tool call.
type TokenSource interface {
	Resolve(ctx context.Context, call tool.Call) (Client, error)
}

// ConnectedSource uses per-user GCP OAuth tokens (cloud-platform.read-only covers GKE API).
type ConnectedSource struct {
	Service  connections.Service
	Defaults Defaults
}

func (s ConnectedSource) Resolve(ctx context.Context, call tool.Call) (Client, error) {
	if s.Service.Store == nil {
		return Client{}, fmt.Errorf("kubernetes connections are not configured")
	}
	if call.Scope.UserID == "" {
		return Client{}, fmt.Errorf("user id is required for kubernetes")
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
		return Client{}, nil, fmt.Errorf("kubernetes is not configured: connect Google Cloud in App Home")
	}
	client, err := source.Resolve(ctx, call)
	if err != nil {
		if result, convErr := toolResult(err); convErr == nil {
			return Client{}, &result, nil
		}
		return Client{}, nil, err
	}
	if strings.TrimSpace(client.AccessToken) == "" {
		return Client{}, nil, fmt.Errorf("kubernetes access token is empty")
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

func (c *Client) session(ctx context.Context, target ClusterTarget) (*clusterSession, error) {
	key := target.Project + "/" + target.Location + "/" + target.Cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.clusters == nil {
		c.clusters = make(map[string]*clusterSession)
	}
	if session, ok := c.clusters[key]; ok {
		return session, nil
	}
	info, err := c.fetchCluster(ctx, target)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSpace(info.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("cluster %s has no API endpoint", target.Cluster)
	}
	if !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	ca, err := base64.StdEncoding.DecodeString(strings.TrimSpace(info.MasterAuth.ClusterCaCertificate))
	if err != nil {
		return nil, fmt.Errorf("decode cluster CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("cluster %s has invalid CA certificate", target.Cluster)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	session := &clusterSession{
		baseURL: strings.TrimRight(endpoint, "/"),
		client:  &http.Client{Timeout: 60 * time.Second, Transport: transport},
	}
	c.clusters[key] = session
	return session, nil
}

type gkeClusterInfo struct {
	Endpoint   string `json:"endpoint"`
	MasterAuth struct {
		ClusterCaCertificate string `json:"clusterCaCertificate"`
	} `json:"masterAuth"`
}

func (c Client) fetchCluster(ctx context.Context, target ClusterTarget) (gkeClusterInfo, error) {
	location := strings.TrimSpace(target.Location)
	if location == "" {
		return gkeClusterInfo{}, fmt.Errorf("cluster location (region or zone) is required; configure GKE_REGION or pass a gke_PROJECT_LOCATION_CLUSTER context")
	}
	rawURL := fmt.Sprintf(
		"https://container.googleapis.com/v1/projects/%s/locations/%s/clusters/%s",
		url.PathEscape(target.Project),
		url.PathEscape(location),
		url.PathEscape(target.Cluster),
	)
	data, err := c.doGCP(ctx, rawURL)
	if err != nil {
		return gkeClusterInfo{}, err
	}
	var info gkeClusterInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return gkeClusterInfo{}, err
	}
	return info, nil
}

func (c Client) doGCP(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gke api failed: %s", strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c Client) doAPI(ctx context.Context, target ClusterTarget, method, apiPath string, query url.Values) ([]byte, error) {
	session, err := c.session(ctx, target)
	if err != nil {
		return nil, err
	}
	rawURL := session.baseURL + apiPath
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := session.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kubernetes api %s failed: %s", apiPath, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c Client) listGKEClusters(ctx context.Context, project, location string) ([]byte, error) {
	if strings.TrimSpace(location) == "" {
		rawURL := fmt.Sprintf("https://container.googleapis.com/v1/projects/%s/locations/-/clusters", url.PathEscape(project))
		return c.doGCP(ctx, rawURL)
	}
	rawURL := fmt.Sprintf(
		"https://container.googleapis.com/v1/projects/%s/locations/%s/clusters",
		url.PathEscape(project),
		url.PathEscape(location),
	)
	return c.doGCP(ctx, rawURL)
}

var errClusterRequired = fmt.Errorf("GCP project and GKE cluster are required; configure GCP_PROJECT and GKE_CLUSTER or pass a gke_PROJECT_LOCATION_CLUSTER context")
