package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/safety"
	"github.com/noknov/slack-copilot-agent/packages/toolkit/tools/registry"
)

// runBase holds shared configuration for Cloud Run tools.
type runBase struct {
	GCloudPath     string
	DefaultProject string
	DefaultRegion  string
	Guard          safety.CommandPolicy
	Timeout        time.Duration
}

func (b runBase) gcloud() string {
	if b.GCloudPath != "" {
		return b.GCloudPath
	}
	return "gcloud"
}

func (b runBase) timeout() time.Duration {
	if b.Timeout > 0 {
		return b.Timeout
	}
	return 30 * time.Second
}

func (b runBase) run(ctx context.Context, args []string, project, region string) (string, error) {
	if project == "" {
		project = b.DefaultProject
	}
	if project == "" {
		return "", fmt.Errorf("GCP project is required; pass project in tool args or configure GCP_PROJECT")
	}
	if region == "" {
		region = b.DefaultRegion
	}

	cmdArgs := append(args, "--project", project)
	if region != "" {
		cmdArgs = append(cmdArgs, "--region", region)
	}
	cmdArgs = append(cmdArgs, "--format", "json")

	display := b.gcloud() + " " + strings.Join(cmdArgs, " ")
	if err := b.Guard.Check(display); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, b.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, b.gcloud(), cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gcloud failed: %s", strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// RunServicesTool — list and describe Cloud Run services.
// ─────────────────────────────────────────────────────────────────────────────

type RunServicesTool struct {
	GCloudPath     string
	DefaultProject string
	DefaultRegion  string
	Guard          safety.CommandPolicy
	Timeout        time.Duration
}

func (RunServicesTool) Parallel() bool { return true }

func (t RunServicesTool) base() runBase {
	return runBase{
		GCloudPath:     t.GCloudPath,
		DefaultProject: t.DefaultProject,
		DefaultRegion:  t.DefaultRegion,
		Guard:          t.Guard,
		Timeout:        t.Timeout,
	}
}

func (t RunServicesTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"gcp-run_services",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"action":  map[string]any{"type": "string", "description": ""},
			"service": map[string]any{"type": "string", "description": ""},
			"project": map[string]any{"type": "string", "description": ""},
			"region":  map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t RunServicesTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Action  string `json:"action"`
		Service string `json:"service"`
		Project string `json:"project"`
		Region  string `json:"region"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}

	action := args.Action
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		out, err := t.base().run(ctx, []string{"run", "services", "list"}, args.Project, args.Region)
		if err != nil {
			return registry.Result{}, err
		}
		return registry.Result{Content: out}, nil
	case "describe":
		if args.Service == "" {
			return registry.Result{}, fmt.Errorf("service name is required for action=describe")
		}
		out, err := t.base().run(ctx, []string{"run", "services", "describe", args.Service}, args.Project, args.Region)
		if err != nil {
			return registry.Result{}, err
		}
		return registry.Result{Content: out}, nil
	default:
		return registry.Result{}, fmt.Errorf("unsupported action %q; use list or describe", action)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RunRevisionsTool — list Cloud Run service revisions and traffic splits.
// ─────────────────────────────────────────────────────────────────────────────

type RunRevisionsTool struct {
	GCloudPath     string
	DefaultProject string
	DefaultRegion  string
	Guard          safety.CommandPolicy
	Timeout        time.Duration
}

func (RunRevisionsTool) Parallel() bool { return true }

func (t RunRevisionsTool) base() runBase {
	return runBase{
		GCloudPath:     t.GCloudPath,
		DefaultProject: t.DefaultProject,
		DefaultRegion:  t.DefaultRegion,
		Guard:          t.Guard,
		Timeout:        t.Timeout,
	}
}

func (t RunRevisionsTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"gcp-run_revisions",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"service": map[string]any{"type": "string", "description": ""},
			"project": map[string]any{"type": "string", "description": ""},
			"region":  map[string]any{"type": "string", "description": ""},
			"limit":   map[string]any{"type": "integer", "description": ""},
		}),
	)
}

func (t RunRevisionsTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Service string `json:"service"`
		Project string `json:"project"`
		Region  string `json:"region"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}

	cmdArgs := []string{"run", "revisions", "list"}
	if args.Service != "" {
		cmdArgs = append(cmdArgs, "--service", args.Service)
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	cmdArgs = append(cmdArgs, fmt.Sprintf("--limit=%d", limit))

	out, err := t.base().run(ctx, cmdArgs, args.Project, args.Region)
	if err != nil {
		return registry.Result{}, err
	}
	return registry.Result{Content: out}, nil
}
