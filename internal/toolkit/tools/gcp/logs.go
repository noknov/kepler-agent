package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/prompts"
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

func (LogsTool) Parallel() bool { return true }

func (t LogsTool) Spec() llm.ToolSpec {
	dynamicHint := ""
	if hint := t.defaultHint(); hint != "" {
		dynamicHint = prompts.PromptText("gcp_defaults_hint_prefix", "") + hint + "."
	}
	return registry.FunctionSpec(
		"gcp-logs",
		dynamicHint,
		registry.ObjectSchema(nil, map[string]any{
			"filter":    map[string]any{"type": "string", "description": ""},
			"severity":  map[string]any{"type": "string", "description": ""},
			"namespace": map[string]any{"type": "string", "description": ""},
			"service":   map[string]any{"type": "string", "description": ""},
			"freshness": map[string]any{"type": "string", "description": ""},
			"limit":     map[string]any{"type": "integer", "description": ""},
			"project":   map[string]any{"type": "string", "description": ""},
			"format":    map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t LogsTool) Execute(ctx context.Context, raw json.RawMessage, rt registry.Runtime) (registry.Result, error) {
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
	if args.Severity != "" && !validSeverity(args.Severity) {
		return registry.Result{}, fmt.Errorf("invalid severity %q", args.Severity)
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
