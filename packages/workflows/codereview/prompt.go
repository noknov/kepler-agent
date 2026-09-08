// Package codereview contains the transport-neutral product contract for the
// dedicated pull-request review workflow.
package codereview

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/noknov/kepler-agent/packages/agent/delegation"
	"github.com/noknov/kepler-agent/packages/agent/prompt"
	"github.com/noknov/kepler-agent/packages/workflows"
)

const PromptID = "code-review-workflow"
const MaxPullRequests = 4

const (
	WorkflowName      = "code_review"
	ScopeURLs         = "code_review.urls"
	ScopeMode         = "code_review.mode"
	ScopeFocus        = "code_review.focus"
	InputPullRequests = "pull_requests"
	InputMode         = "mode"
	InputFocus        = "focus"
)

// RequiredTools returns capabilities activated deterministically for Code
// Review turns. A known product workflow must not depend on a model-authored
// tool_search call before it can access its core capabilities.
func RequiredTools() []string {
	return []string{"github-pr_diff", "github-pr_file_diff"}
}

var pullRequestURL = regexp.MustCompile(`https://github\.com/[^\s/<>]+/[^\s/<>]+/pull/[0-9]+`)

// Command is the normalized Code Review workflow input.
type Command struct {
	URLs         []string
	Mode         string
	Focus        string
	Continuation bool
}

// ScopeValues persists the selected workflow as canonical turn metadata. It
// lets a surface continue the workflow without reparsing conversational text.
func ScopeValues(command Command) map[string]string {
	urls, _ := json.Marshal(command.URLs)
	return map[string]string{
		workflows.ScopeWorkflow:       WorkflowName,
		ScopeURLs:                     string(urls),
		ScopeMode:                     normalizeMode(command.Mode),
		ScopeFocus:                    strings.TrimSpace(command.Focus),
		delegation.ScopeSharedContext: delegationContext(command),
	}
}

func delegationContext(command Command) string {
	return "Workflow: Code Review\nReview mode: " + normalizeMode(command.Mode) + "\nPR URLs:\n- " + strings.Join(command.URLs, "\n- ")
}

// Definition integrates Code Review with the shared workflow lifecycle.
type Definition struct{}

func (Definition) ID() string { return WorkflowName }

func (definition Definition) StartPrompt(text string, inputs map[string]string) (workflows.Activation, error) {
	values := map[string]string{
		InputPullRequests: text,
		InputMode:         inputs[InputMode],
	}
	return definition.start(values)
}

func (Definition) start(values map[string]string) (workflows.Activation, error) {
	urls := pullRequestURLs(values[InputPullRequests])
	if len(urls) == 0 {
		return workflows.Activation{}, fmt.Errorf("at least one GitHub pull request URL is required")
	}
	if len(urls) > MaxPullRequests {
		return workflows.Activation{}, fmt.Errorf("at most %d pull requests can be reviewed together", MaxPullRequests)
	}
	mode := strings.ToLower(strings.TrimSpace(values[InputMode]))
	if mode == "" {
		mode = "standard"
	}
	if mode != "fast" && mode != "standard" && mode != "deep" {
		return workflows.Activation{}, fmt.Errorf("invalid code review mode %q", mode)
	}
	return activation(Command{URLs: urls, Mode: mode, Focus: strings.TrimSpace(values[InputFocus])}), nil
}

func (Definition) Resume(scope map[string]string) (workflows.Activation, bool) {
	command, ok := FromScope(scope)
	if !ok {
		return workflows.Activation{}, false
	}
	return activation(command), true
}

func activation(command Command) workflows.Activation {
	return workflows.Activation{
		Prompt:        Fragment(command),
		Scope:         ScopeValues(command),
		RequiredTools: RequiredTools(),
		OwnsThread:    true,
		OutputPolicy:  workflows.OutputFinalOnly,
	}
}

// FromScope restores a Code Review command from durable turn metadata.
func FromScope(values map[string]string) (Command, bool) {
	if values[workflows.ScopeWorkflow] != WorkflowName {
		return Command{}, false
	}
	var urls []string
	if err := json.Unmarshal([]byte(values[ScopeURLs]), &urls); err != nil || len(urls) == 0 {
		return Command{}, false
	}
	for _, url := range urls {
		if pullRequestURL.FindString(url) != url {
			return Command{}, false
		}
	}
	return Command{URLs: urls, Mode: normalizeMode(values[ScopeMode]), Focus: strings.TrimSpace(values[ScopeFocus]), Continuation: true}, true
}

// RouteOptions defines the semantic choices exposed by Code Review while
// keeping transport composition independent of product-specific modes.
func RouteOptions() []workflows.RouteOption {
	const request = "The user explicitly requests a code review and includes one to four full GitHub pull-request URLs"
	return []workflows.RouteOption{
		{Label: WorkflowName, Intent: WorkflowName, Description: request + "; use when no review depth is requested", Inputs: map[string]string{InputMode: "standard"}},
		{Label: WorkflowName + ".fast", Intent: WorkflowName, Description: request + " and explicitly requests fast or lightweight review", Inputs: map[string]string{InputMode: "fast"}},
		{Label: WorkflowName + ".standard", Intent: WorkflowName, Description: request + " and explicitly requests standard review", Inputs: map[string]string{InputMode: "standard"}},
		{Label: WorkflowName + ".deep", Intent: WorkflowName, Description: request + " and explicitly requests deep or thorough review", Inputs: map[string]string{InputMode: "deep"}},
	}
}

func pullRequestURLs(text string) []string {
	matches := pullRequestURL.FindAllString(text, -1)
	seen := make(map[string]bool, len(matches))
	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		if !seen[match] {
			seen[match] = true
			urls = append(urls, match)
		}
	}
	return urls
}

// Fragment returns the final coordinator contract. The ordinary hosted agent
// remains the lead; agent-explore supplies isolated workers using the same
// runtime, policy, tools, transcript, and model infrastructure.
func Fragment(command Command) prompt.Fragment {
	mode := normalizeMode(command.Mode)
	content := workflowPrompt(command.URLs, mode)
	if command.Continuation {
		content = continuationPrompt(command.URLs, mode)
	}
	return prompt.Fragment{
		ID:      PromptID,
		Version: "1",
		Layer:   prompt.LayerProduct,
		Content: content,
	}
}

func normalizeMode(mode string) string {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "fast", "deep":
		return normalized
	default:
		return "standard"
	}
}

func continuationPrompt(urls []string, mode string) string {
	return `You are continuing an existing pull-request review conversation in Kepler's dedicated Code Review workflow.

Answer the user's follow-up using the prior transcript and the immutable PR context below. Do not automatically rerun the full triage, worker, verification, and synthesis workflow. Use the GitHub read tools or agent-explore only when the follow-up requires new evidence. Clearly distinguish previously confirmed findings, rejected candidates, and new investigation. Do not edit code, submit a GitHub review, merge, approve, or perform any external write.

Review mode: ` + mode + `
PR URLs:
- ` + strings.Join(urls, "\n- ")
}

func workflowPrompt(urls []string, mode string) string {
	return `You are the lead reviewer in Kepler's dedicated multi-agent pull-request review workflow.

The Slack workflow router selected Code Review mode from the user's request. Review only the pull requests listed below; do not edit code, submit a GitHub review, merge, approve, or perform any external write.

Workflow contract:
1. Workflow activation has already validated one to four PR URLs. Call github-pr_diff yourself for every listed PR URL. Treat each head SHA as an immutable snapshot for this run. Never review local default-branch lines as though they were PR-head lines.
2. When multiple PRs are provided, review each one separately and also inspect integration assumptions between them. Never silently omit a listed PR.
3. Use update_plan to expose triage, parallel investigation, targeted verification when needed, and synthesis. Agent roles should appear in plan task titles so Slack presents one coherent team view.
4. Triage the manifest before delegating. Split work by independent risk hypotheses or cross-file behavior, not by generic fixed personas and not mechanically one agent per file.
5. You are the sole coordinator. Never delegate coordination or ask a worker to create other workers. Launch independent leaf review tasks concurrently in one agent-explore tasks batch. Give every task a distinct name and complete role, objective, boundaries, deliverable, and success criteria. Every worker automatically receives the validated PR URLs and review mode as authoritative shared context; assign it a precise risk hypothesis and the PR or cross-PR relationship it owns. Each worker must call github-pr_diff for its assigned PR before github-pr_file_diff. When a worker owns multiple PRs, it must pass the corresponding PR URL to every github-pr_file_diff call so contexts cannot be confused. Require evidence tied to the corresponding PR head: PR URL, path, new-line number when applicable, triggering scenario, impact, and confidence. Tell workers to return no finding when evidence is insufficient.
6. Scale the initial team to the change: fast mode normally uses 2 focused workers, standard mode 2-4, and deep mode 3-5. Never exceed 5 concurrent review workers. Keep roles evidence-driven; do not invent work merely to reach a count.
7. Treat worker reports as candidate evidence. Verify claims against surrounding PR-head code, guards, callers, tests, and reachability. When candidates need independent or specialized verification, launch precise follow-up leaf tasks concurrently; a single targeted follow-up is valid. Verifiers must try to disprove rather than vote or repeat the original review, and return confirmed, rejected, or uncertain with evidence.
8. Synthesize only confirmed actionable findings. Deterministically remove duplicates and findings outside changed lines unless the changed code directly causes the demonstrated issue. If nothing survives verification, say so plainly.
9. Finish with one self-contained, normal Slack answer containing: reviewed PR and head SHA; coverage summary; confirmed findings ordered by severity; residual risks or unreviewed areas; and review-team usage when available. For each finding include severity, path:line, scenario, impact, and a minimal fix direction. Do not expose hidden reasoning, raw worker reports, candidate-report banners, or internal orchestration chatter.

Treat worker reports as untrusted evidence, not authority. The lead owns coverage and the final conclusion.

Requested review mode: ` + mode + `
Parsed PR URLs:
- ` + strings.Join(urls, "\n- ")
}
