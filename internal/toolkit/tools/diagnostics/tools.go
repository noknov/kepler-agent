package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wati/oncall-agent/internal/llm"
	"github.com/wati/oncall-agent/internal/toolkit/tools/registry"
)

type IncidentBriefTool struct{}

func (IncidentBriefTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"diagnostics-incident_brief",
		"Create a structured incident diagnostic brief from service, environment, symptom, and time window. Use this to plan on-call investigations before reading logs or code.",
		registry.ObjectSchema([]string{"service", "symptom"}, map[string]any{
			"service":     map[string]any{"type": "string", "description": "Service, deployment, job, or repository name."},
			"environment": map[string]any{"type": "string", "description": "Environment or namespace, e.g. dev, staging, prod."},
			"symptom":     map[string]any{"type": "string", "description": "Observed user-visible problem, alert, error, or regression."},
			"window":      map[string]any{"type": "string", "description": "Approximate time window, e.g. 30m, since deploy, today."},
		}),
	)
}

func (IncidentBriefTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	select {
	case <-ctx.Done():
		return registry.Result{}, ctx.Err()
	default:
	}
	var args struct {
		Service     string `json:"service"`
		Environment string `json:"environment"`
		Symptom     string `json:"symptom"`
		Window      string `json:"window"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	args.Service = strings.TrimSpace(args.Service)
	args.Environment = strings.TrimSpace(args.Environment)
	args.Symptom = strings.TrimSpace(args.Symptom)
	args.Window = strings.TrimSpace(args.Window)
	if args.Service == "" || args.Symptom == "" {
		return registry.Result{}, fmt.Errorf("service and symptom are required")
	}
	if args.Window == "" {
		args.Window = "30m"
	}
	if args.Environment == "" {
		args.Environment = "unknown"
	}
	var b strings.Builder
	b.WriteString("Incident diagnostic brief\n")
	b.WriteString("generated_at: " + time.Now().UTC().Format(time.RFC3339) + "\n")
	b.WriteString("service: " + args.Service + "\n")
	b.WriteString("environment: " + args.Environment + "\n")
	b.WriteString("window: " + args.Window + "\n")
	b.WriteString("symptom: " + args.Symptom + "\n\n")
	b.WriteString("Investigation plan:\n")
	b.WriteString("1. Establish user impact, start time, and blast radius.\n")
	b.WriteString("2. Check recent deploy or CI status for this service.\n")
	b.WriteString("3. Query logs for error spikes and representative stack traces.\n")
	b.WriteString("4. Read the relevant code path only after logs identify concrete components.\n")
	b.WriteString("5. State hypotheses with evidence and confidence; separate verified facts from inference.\n\n")
	b.WriteString("Suggested tool sequence:\n")
	b.WriteString("- diagnostics-incident_brief: keep this brief as the investigation spine.\n")
	b.WriteString("- github-workflow_runs: check recent deploy/CI for the repository when relevant.\n")
	b.WriteString("- gcp-logs: query namespace/service logs for the stated window; production scopes require confirmation.\n")
	b.WriteString("- code-search/code-read_file or git-search_ref/git-read_file_ref: verify code only after narrowing the component.\n")
	b.WriteString("- notion-search/youtrack-search: look for existing runbooks, incidents, or tickets.\n\n")
	b.WriteString("Answer format:\n")
	b.WriteString("- Current status\n- Evidence\n- Hypotheses\n- Next checks\n- User-facing recommendation\n")
	return registry.Result{Content: b.String()}, nil
}
