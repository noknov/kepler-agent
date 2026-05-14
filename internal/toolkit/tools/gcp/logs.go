package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/safety"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type LogsTool struct {
	GCloudPath       string
	DefaultProject   string
	DefaultNamespace string
	DefaultCluster   string
	DefaultRegion    string
	Guard            safety.CommandPolicy
	Timeout          time.Duration
}

func (t LogsTool) Spec() llm.ToolSpec {
	description := "Query GCP Cloud Logging with gcloud logging read. Read-only. Project, namespace, service, and filter are per-call inputs; configured GCP values are only defaults/hints, not fixed environments."
	if hint := t.defaultHint(); hint != "" {
		description += " Current defaults/hints: " + hint + "."
	}
	return registry.FunctionSpec(
		"gcp-logs",
		description,
		registry.ObjectSchema(nil, map[string]any{
			"filter":    map[string]any{"type": "string", "description": "Cloud Logging filter expression. Example: severity>=ERROR AND resource.labels.namespace_name=\"mt-dev\""},
			"severity":  map[string]any{"type": "string", "description": "Minimum severity, e.g. ERROR, WARNING, INFO. Used when filter is not fully specified."},
			"namespace": map[string]any{"type": "string", "description": "GKE namespace for this query. Optional; when omitted, no namespace filter is added unless filter already contains one."},
			"service":   map[string]any{"type": "string", "description": "Service/deployment/container name hint. Matched against common GKE labels."},
			"freshness": map[string]any{"type": "string", "description": "Freshness window, e.g. 30m, 2h. Defaults to 30m."},
			"limit":     map[string]any{"type": "integer", "description": "Maximum log entries. Defaults to 30, max 200."},
			"project":   map[string]any{"type": "string", "description": "GCP project for this query. Defaults to configured GCP_PROJECT only when omitted."},
			"format":    map[string]any{"type": "string", "description": "gcloud format, json or value. Defaults to json."},
		}),
	)
}

func (t LogsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
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
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	project := args.Project
	if project == "" {
		project = t.DefaultProject
	}
	if project == "" {
		return registry.Result{}, fmt.Errorf("GCP project is required; pass project in tool args or configure GCP_PROJECT as a default")
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
	filter := buildFilter(args.Filter, args.Severity, args.Namespace, args.Service)
	format := args.Format
	if format != "value" {
		format = "json"
	}
	bin := t.GCloudPath
	if bin == "" {
		bin = "gcloud"
	}
	cmdArgs := []string{
		"logging", "read", filter,
		"--project", project,
		"--freshness", args.Freshness,
		"--limit", strconv.Itoa(args.Limit),
		"--format", format,
	}
	display := bin + " " + strings.Join(cmdArgs, " ")
	if err := t.Guard.Check(display); err != nil {
		return registry.Result{}, err
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return registry.Result{}, fmt.Errorf("gcloud logging read failed: %s", strings.TrimSpace(stderr.String()))
	}
	return registry.Result{Content: stdout.String()}, nil
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
		parts = append(parts, `resource.labels.namespace_name="`+namespace+`"`)
	}
	if service != "" {
		parts = append(parts, `(
resource.labels.container_name="`+service+`" OR
resource.labels.pod_name:"`+service+`" OR
labels."k8s-pod/app"="`+service+`" OR
labels."k8s-pod/app_kubernetes_io/name"="`+service+`"
)`)
	}
	return strings.Join(parts, " AND ")
}

func (t LogsTool) defaultHint() string {
	parts := make([]string, 0, 4)
	if t.DefaultProject != "" {
		parts = append(parts, "project="+t.DefaultProject)
	}
	if t.DefaultNamespace != "" {
		parts = append(parts, "namespace="+t.DefaultNamespace)
	}
	if t.DefaultCluster != "" {
		parts = append(parts, "cluster="+t.DefaultCluster)
	}
	if t.DefaultRegion != "" {
		parts = append(parts, "region="+t.DefaultRegion)
	}
	return strings.Join(parts, ", ")
}
