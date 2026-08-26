package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/noknov/kepler-agent/packages/agent/tool"
	"github.com/noknov/kepler-agent/packages/prompts"
)

type LogsTool struct {
	Source   TokenSource
	Defaults Defaults
	Timeout  time.Duration
}

func (t LogsTool) Descriptor() tool.Descriptor {
	dynamicHint := ""
	if hint := t.defaultHint(); hint != "" {
		dynamicHint = prompts.PromptText("gcp_defaults_hint_prefix", "") + hint + "."
	}
	return tool.FunctionDescriptor(
		"gcp-logs",
		dynamicHint,
		tool.ObjectSchema(nil, map[string]any{
			"filter":    map[string]any{"type": "string", "description": ""},
			"severity":  map[string]any{"type": "string", "description": ""},
			"namespace": map[string]any{"type": "string", "description": ""},
			"service":   map[string]any{"type": "string", "description": ""},
			"freshness": map[string]any{"type": "string", "description": ""},
			"limit":     map[string]any{"type": "integer", "description": ""},
			"project":   map[string]any{"type": "string", "description": ""},
			"format":    map[string]any{"type": "string", "description": ""},
		}),
		tool.ReadNetworkParallel()...,
	)
}

func (t LogsTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	var args struct {
		Filter    string `json:"filter"`
		Severity  string `json:"severity"`
		Namespace string `json:"namespace"`
		Service   string `json:"service"`
		Freshness string `json:"freshness"`
		Limit     int    `json:"limit"`
		Project   string `json:"project"`
		Format    string `json:"format"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Freshness == "" {
		args.Freshness = "30m"
	}
	if args.Limit <= 0 {
		args.Limit = 30
	}
	if args.Limit > 200 {
		args.Limit = 200
	}
	if args.Severity != "" && !validSeverity(args.Severity) {
		return tool.Result{}, fmt.Errorf("invalid severity %q", args.Severity)
	}
	filter := buildFilter(args.Filter, args.Severity, args.Namespace, args.Service)
	if freshnessClause, err := freshnessFilter(args.Freshness); err != nil {
		return tool.Result{}, err
	} else if freshnessClause != "" {
		filter = joinFilters(filter, freshnessClause)
	}

	client, pending, err := begin(ctx, t.Source, call)
	if pending != nil {
		return *pending, nil
	}
	if err != nil {
		return tool.Result{}, err
	}
	project, err := client.projectID(args.Project)
	if err != nil {
		return tool.Result{}, err
	}
	timeout := t.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := client.listLogEntries(ctx, project, filter, args.Limit)
	if err != nil {
		return tool.Result{}, err
	}
	if strings.TrimSpace(args.Format) == "value" {
		return tool.TextResult(formatLogEntriesValue(data)), nil
	}
	return tool.TextResult(string(data)), nil
}

func (t LogsTool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 30 * time.Second
}

func (t LogsTool) defaultHint() string {
	parts := make([]string, 0, 4)
	if t.Defaults.Project != "" {
		parts = append(parts, "project="+t.Defaults.Project)
	}
	if t.Defaults.Namespace != "" {
		parts = append(parts, "namespace="+t.Defaults.Namespace)
	}
	if t.Defaults.Cluster != "" {
		parts = append(parts, "cluster="+t.Defaults.Cluster)
	}
	if t.Defaults.Region != "" {
		parts = append(parts, "region="+t.Defaults.Region)
	}
	return strings.Join(parts, ", ")
}

func buildFilter(raw, severity, namespace, service string) string {
	parts := make([]string, 0, 4)
	hasRaw := strings.TrimSpace(raw) != ""
	if strings.TrimSpace(raw) != "" {
		parts = append(parts, "("+strings.TrimSpace(raw)+")")
	}
	if severity == "" && !hasRaw {
		severity = "ERROR"
	}
	if severity != "" {
		parts = append(parts, "severity>="+severity)
	}
	if namespace != "" {
		parts = append(parts, `resource.labels.namespace_name="`+filterString(namespace)+`"`)
	}
	if service != "" {
		service = filterString(service)
		parts = append(parts, `(
resource.labels.container_name="`+service+`" OR
resource.labels.pod_name:"`+service+`" OR
labels."k8s-pod/app"="`+service+`" OR
labels."k8s-pod/app_kubernetes_io/name"="`+service+`"
)`)
	}
	return strings.Join(parts, " AND ")
}

func joinFilters(parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, " AND ")
}

func freshnessFilter(freshness string) (string, error) {
	d, err := parseFreshness(freshness)
	if err != nil {
		return "", err
	}
	if d <= 0 {
		return "", nil
	}
	start := time.Now().UTC().Add(-d)
	return fmt.Sprintf(`timestamp>="%s"`, start.Format("2006-01-02T15:04:05Z")), nil
}

var freshnessRe = regexp.MustCompile(`^(\d+)([smhd])$`)

func parseFreshness(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, nil
	}
	matches := freshnessRe.FindStringSubmatch(raw)
	if len(matches) != 3 {
		return 0, fmt.Errorf("invalid freshness %q; use formats like 30m, 1h, 2d", raw)
	}
	value, err := strconv.Atoi(matches[1])
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid freshness %q", raw)
	}
	switch matches[2] {
	case "s":
		return time.Duration(value) * time.Second, nil
	case "m":
		return time.Duration(value) * time.Minute, nil
	case "h":
		return time.Duration(value) * time.Hour, nil
	case "d":
		return time.Duration(value) * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("invalid freshness %q", raw)
	}
}

var severityRe = regexp.MustCompile(`^[A-Za-z]+$`)

func validSeverity(severity string) bool {
	return severityRe.MatchString(strings.TrimSpace(severity))
}

func filterString(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return value
}

func formatLogEntriesValue(data []byte) string {
	var payload struct {
		Entries []struct {
			Timestamp string `json:"timestamp"`
			Severity  string `json:"severity"`
			Text      string `json:"textPayload"`
			JSON      any    `json:"jsonPayload"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return string(data)
	}
	var lines []string
	for _, entry := range payload.Entries {
		line := strings.TrimSpace(entry.Timestamp)
		if entry.Severity != "" {
			line += " " + entry.Severity
		}
		if entry.Text != "" {
			line += " " + entry.Text
		} else if entry.JSON != nil {
			if encoded, err := json.Marshal(entry.JSON); err == nil {
				line += " " + string(encoded)
			}
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	return strings.Join(lines, "\n")
}
