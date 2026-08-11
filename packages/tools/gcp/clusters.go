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
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

// ClustersTool wraps `gcloud container clusters list/describe` to inspect
// GKE cluster state, version, node count, and health without needing kubectl
// credentials pre-configured.
type ClustersTool struct {
	GCloudPath     string
	DefaultProject string
	DefaultRegion  string
	Guard          safety.CommandPolicy
	Timeout        time.Duration
}

func (ClustersTool) Parallel() bool { return true }

func (t ClustersTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"gcp-clusters",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"action":  map[string]any{"type": "string", "description": ""},
			"cluster": map[string]any{"type": "string", "description": ""},
			"project": map[string]any{"type": "string", "description": ""},
			"region":  map[string]any{"type": "string", "description": ""},
			"zone":    map[string]any{"type": "string", "description": ""},
		}),
	)
}

func (t ClustersTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	var args struct {
		Action  string `json:"action"`
		Cluster string `json:"cluster"`
		Project string `json:"project"`
		Region  string `json:"region"`
		Zone    string `json:"zone"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}

	project := args.Project
	if project == "" {
		project = t.DefaultProject
	}
	if project == "" {
		return registry.Result{}, fmt.Errorf("GCP project is required; pass project in tool args or configure GCP_PROJECT")
	}

	action := args.Action
	if action == "" {
		action = "list"
	}
	switch action {
	case "list", "describe":
	default:
		return registry.Result{}, fmt.Errorf("unsupported action %q; use list or describe", action)
	}

	var cmdArgs []string
	if action == "list" {
		cmdArgs = []string{"container", "clusters", "list", "--project", project}
	} else {
		if args.Cluster == "" {
			return registry.Result{}, fmt.Errorf("cluster name is required for action=describe")
		}
		cmdArgs = []string{"container", "clusters", "describe", args.Cluster, "--project", project}
	}

	region := args.Region
	if region == "" {
		region = t.DefaultRegion
	}
	if args.Zone != "" {
		cmdArgs = append(cmdArgs, "--zone", args.Zone)
	} else if region != "" {
		cmdArgs = append(cmdArgs, "--region", region)
	}
	cmdArgs = append(cmdArgs, "--format", "json")

	bin := t.GCloudPath
	if bin == "" {
		bin = "gcloud"
	}
	if err := t.Guard.CheckArgv(append([]string{bin}, cmdArgs...)); err != nil {
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
		return registry.Result{}, fmt.Errorf("gcloud container clusters failed: %s", strings.TrimSpace(stderr.String()))
	}
	return registry.Result{Content: stdout.String()}, nil
}
