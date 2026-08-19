package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (c Client) listPods(ctx context.Context, target ClusterTarget, allNamespaces bool, labelSelector, fieldSelector string) ([]byte, error) {
	query := url.Values{}
	if labelSelector != "" {
		query.Set("labelSelector", labelSelector)
	}
	if fieldSelector != "" {
		query.Set("fieldSelector", fieldSelector)
	}
	if allNamespaces {
		return c.doAPI(ctx, target, "GET", "/api/v1/pods", query)
	}
	ns := target.Namespace
	if ns == "" {
		return c.doAPI(ctx, target, "GET", "/api/v1/pods", query)
	}
	return c.doAPI(ctx, target, "GET", "/api/v1/namespaces/"+url.PathEscape(ns)+"/pods", query)
}

func (c Client) listEvents(ctx context.Context, target ClusterTarget, allNamespaces bool, fieldSelectors []string) ([]byte, error) {
	query := url.Values{}
	if len(fieldSelectors) > 0 {
		query.Set("fieldSelector", strings.Join(fieldSelectors, ","))
	}
	if allNamespaces {
		return c.doAPI(ctx, target, "GET", "/api/v1/events", query)
	}
	ns := target.Namespace
	if ns == "" {
		return c.doAPI(ctx, target, "GET", "/api/v1/events", query)
	}
	return c.doAPI(ctx, target, "GET", "/api/v1/namespaces/"+url.PathEscape(ns)+"/events", query)
}

func (c Client) getResource(ctx context.Context, target ClusterTarget, apiPath string, query url.Values) ([]byte, error) {
	return c.doAPI(ctx, target, "GET", apiPath, query)
}

func (c Client) podLogs(ctx context.Context, target ClusterTarget, pod, container string, tail int, since string, previous, timestamps bool) (string, error) {
	ns := target.Namespace
	if ns == "" {
		return "", fmt.Errorf("namespace is required for pod logs")
	}
	query := url.Values{}
	if container != "" {
		query.Set("container", container)
	}
	if tail > 0 {
		query.Set("tailLines", fmt.Sprintf("%d", tail))
	}
	if since != "" {
		seconds, err := sinceSeconds(since)
		if err != nil {
			return "", err
		}
		if seconds > 0 {
			query.Set("sinceSeconds", fmt.Sprintf("%d", seconds))
		}
	}
	if previous {
		query.Set("previous", "true")
	}
	if timestamps {
		query.Set("timestamps", "true")
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/log", url.PathEscape(ns), url.PathEscape(pod))
	session, err := c.session(ctx, target)
	if err != nil {
		return "", err
	}
	rawURL := session.baseURL + path
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	resp, err := session.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("pod logs failed: %s", strings.TrimSpace(string(data)))
	}
	return string(data), nil
}

func (c Client) podLogsBySelector(ctx context.Context, target ClusterTarget, labelSelector string, opts podLogOptions) (string, error) {
	pods, err := c.listPods(ctx, target, false, labelSelector, "")
	if err != nil {
		return "", err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(pods, &list); err != nil {
		return "", err
	}
	if len(list.Items) == 0 {
		return "(no pods matched label selector)", nil
	}
	var parts []string
	limit := len(list.Items)
	if limit > 5 {
		limit = 5
	}
	for i := 0; i < limit; i++ {
		name := list.Items[i].Metadata.Name
		text, err := c.podLogs(ctx, target, name, opts.container, opts.tail, opts.since, opts.previous, opts.timestamps)
		if err != nil {
			parts = append(parts, fmt.Sprintf("=== pod/%s (error: %v) ===", name, err))
			continue
		}
		parts = append(parts, fmt.Sprintf("=== pod/%s ===\n%s", name, text))
	}
	if len(list.Items) > limit {
		parts = append(parts, fmt.Sprintf("(truncated: showing %d of %d matching pods)", limit, len(list.Items)))
	}
	return strings.Join(parts, "\n"), nil
}

type podLogOptions struct {
	container  string
	tail       int
	since      string
	previous   bool
	timestamps bool
}

func (c Client) metricsTop(ctx context.Context, target ClusterTarget, resource string, name, labelSelector string) ([]byte, error) {
	query := url.Values{}
	if labelSelector != "" {
		query.Set("labelSelector", labelSelector)
	}
	switch resource {
	case "nodes", "node":
		path := "/apis/metrics.k8s.io/v1beta1/nodes"
		if name != "" {
			path += "/" + url.PathEscape(name)
		}
		return c.doAPI(ctx, target, "GET", path, query)
	default:
		ns := target.Namespace
		if ns == "" {
			return nil, fmt.Errorf("namespace is required for pod metrics")
		}
		path := fmt.Sprintf("/apis/metrics.k8s.io/v1beta1/namespaces/%s/pods", url.PathEscape(ns))
		if name != "" {
			path += "/" + url.PathEscape(name)
		}
		return c.doAPI(ctx, target, "GET", path, query)
	}
}

func (c Client) deploymentRollout(ctx context.Context, target ClusterTarget, name, kind, action string, revision int) (string, error) {
	ns := target.Namespace
	if ns == "" {
		return "", fmt.Errorf("namespace is required")
	}
	kind = normalizeWorkloadKind(kind)
	apiBase := workloadAPIBase(kind)
	path := fmt.Sprintf("%s/namespaces/%s/%s/%s", apiBase, url.PathEscape(ns), workloadResourceName(kind), url.PathEscape(name))
	data, err := c.getResource(ctx, target, path, nil)
	if err != nil {
		return "", err
	}
	switch action {
	case "status":
		return formatRolloutStatus(data, kind, name)
	case "history":
		return c.rolloutHistory(ctx, target, name, kind, revision, data)
	default:
		return "", fmt.Errorf("unsupported rollout action %q", action)
	}
}

func (c Client) rolloutHistory(ctx context.Context, target ClusterTarget, deployName, kind string, revision int, deployRaw []byte) (string, error) {
	var deploy struct {
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
	}
	if err := json.Unmarshal(deployRaw, &deploy); err != nil {
		return "", err
	}
	selector := labelsToSelector(deploy.Selector.MatchLabels)
	query := url.Values{"labelSelector": {selector}}
	ns := target.Namespace
	path := fmt.Sprintf("/apis/apps/v1/namespaces/%s/replicasets", url.PathEscape(ns))
	data, err := c.getResource(ctx, target, path, query)
	if err != nil {
		return "", err
	}
	if revision > 0 {
		return formatRevisionDetail(data, revision)
	}
	return formatRolloutHistory(data)
}

func sinceSeconds(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	d, err := parseDuration(raw)
	if err != nil {
		return 0, err
	}
	return int64(d.Seconds()), nil
}

func parseDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, nil
	}
	// Support kubectl-style 5m, 1h, 30s, 2d
	if strings.HasSuffix(raw, "d") {
		n, err := parseLeadingInt(strings.TrimSuffix(raw, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", raw)
	}
	return d, nil
}

func parseLeadingInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("invalid duration")
	}
	var n int
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		n = n*10 + int(ch-'0')
	}
	return n, nil
}
