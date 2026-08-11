package diagnostics

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/llm"
	"github.com/noknov/slack-copilot-agent/packages/tools/registry"
)

type IncidentBriefTool struct{}

func (IncidentBriefTool) Parallel() bool { return true }

func (IncidentBriefTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"diagnostics-incident_brief",
		"",
		registry.ObjectSchema([]string{"service", "symptom"}, map[string]any{
			"service":     map[string]any{"type": "string", "description": ""},
			"environment": map[string]any{"type": "string", "description": ""},
			"symptom":     map[string]any{"type": "string", "description": ""},
			"window":      map[string]any{"type": "string", "description": ""},
		}),
	)
}

type TimelineTool struct{}

func (TimelineTool) Parallel() bool { return true }

func (TimelineTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"diagnostics-timeline",
		"",
		registry.ObjectSchema([]string{"events"}, map[string]any{
			"title": map[string]any{"type": "string", "description": ""},
			"events": map[string]any{
				"type":        "array",
				"description": "",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"time":     map[string]any{"type": "string"},
						"source":   map[string]any{"type": "string"},
						"event":    map[string]any{"type": "string"},
						"impact":   map[string]any{"type": "string"},
						"evidence": map[string]any{"type": "string"},
					},
				},
			},
		}),
	)
}

func (TimelineTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	select {
	case <-ctx.Done():
		return registry.Result{}, ctx.Err()
	default:
	}
	var args struct {
		Title  string `json:"title"`
		Events []struct {
			Time     string `json:"time"`
			Source   string `json:"source"`
			Event    string `json:"event"`
			Impact   string `json:"impact"`
			Evidence string `json:"evidence"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	if len(args.Events) == 0 {
		return registry.Result{}, fmt.Errorf("events are required")
	}
	title := strings.TrimSpace(args.Title)
	if title == "" {
		title = "Incident timeline"
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	for i, event := range args.Events {
		when := strings.TrimSpace(event.Time)
		if when == "" {
			when = "unknown time"
		}
		source := strings.TrimSpace(event.Source)
		if source == "" {
			source = "unknown source"
		}
		b.WriteString(fmt.Sprintf("%d. %s — %s\n", i+1, when, strings.TrimSpace(event.Event)))
		b.WriteString("   source: " + source + "\n")
		if strings.TrimSpace(event.Impact) != "" {
			b.WriteString("   impact: " + strings.TrimSpace(event.Impact) + "\n")
		}
		if strings.TrimSpace(event.Evidence) != "" {
			b.WriteString("   evidence: " + strings.TrimSpace(event.Evidence) + "\n")
		}
	}
	return registry.Result{Content: b.String()}, nil
}

type EvidenceBoardTool struct{}

func (EvidenceBoardTool) Parallel() bool { return true }

func (EvidenceBoardTool) Spec() llm.ToolSpec {
	return registry.FunctionSpec(
		"diagnostics-evidence_board",
		"",
		registry.ObjectSchema(nil, map[string]any{
			"facts":       arrayOfStrings(),
			"hypotheses":  arrayOfStrings(),
			"risks":       arrayOfStrings(),
			"next_checks": arrayOfStrings(),
		}),
	)
}

func (EvidenceBoardTool) Execute(ctx context.Context, raw json.RawMessage, _ registry.Runtime) (registry.Result, error) {
	select {
	case <-ctx.Done():
		return registry.Result{}, ctx.Err()
	default:
	}
	var args struct {
		Facts      []string `json:"facts"`
		Hypotheses []string `json:"hypotheses"`
		Risks      []string `json:"risks"`
		NextChecks []string `json:"next_checks"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return registry.Result{}, err
	}
	var b strings.Builder
	writeList := func(title string, items []string) {
		b.WriteString(title + "\n")
		if len(items) == 0 {
			b.WriteString("- none captured\n")
			return
		}
		for _, item := range items {
			item = strings.TrimSpace(item)
			if item != "" {
				b.WriteString("- " + item + "\n")
			}
		}
	}
	writeList("Verified facts", args.Facts)
	writeList("Hypotheses", args.Hypotheses)
	writeList("Risks", args.Risks)
	writeList("Next checks", args.NextChecks)
	return registry.Result{Content: b.String()}, nil
}

func arrayOfStrings() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "",
		"items":       map[string]any{"type": "string"},
	}
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
	b.WriteString("- gcp-logs: query namespace/service logs for the stated window.\n")
	b.WriteString("- code-search/code-read_file or git-search_ref/git-read_file_ref: verify code only after narrowing the component.\n")
	b.WriteString("- notion-search/youtrack-search: look for existing runbooks, incidents, or tickets.\n\n")
	b.WriteString("Answer format:\n")
	b.WriteString("- Current status\n- Evidence\n- Hypotheses\n- Next checks\n- User-facing recommendation\n")
	return registry.Result{Content: b.String()}, nil
}
