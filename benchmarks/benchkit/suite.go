package benchkit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadSuite(path string) (Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, err
	}
	var suite Suite
	if err := json.Unmarshal(data, &suite); err != nil {
		return Suite{}, err
	}
	baseDir := filepath.Dir(path)
	for i := range suite.Cases {
		if suite.Cases[i].Workspace != "" && !filepath.IsAbs(suite.Cases[i].Workspace) {
			suite.Cases[i].Workspace = filepath.Clean(filepath.Join(baseDir, suite.Cases[i].Workspace))
		}
	}
	if err := suite.Validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func (s Suite) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("suite name is required")
	}
	if len(s.Cases) == 0 {
		return fmt.Errorf("suite %q has no cases", s.Name)
	}
	seen := map[string]bool{}
	for i, c := range s.Cases {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("case %d id is required", i)
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if strings.TrimSpace(c.Category) == "" {
			return fmt.Errorf("case %q category is required", c.ID)
		}
		if strings.TrimSpace(c.Prompt) == "" {
			return fmt.Errorf("case %q prompt is required", c.ID)
		}
	}
	return nil
}

func BuiltinSuite() Suite {
	return Suite{
		Name:        "architecture-smoke",
		Description: "Small local suite for validating code reading, debugging, and architecture reasoning against this repository.",
		Cases: []Case{
			{
				ID:       "read-service-boundaries",
				Category: "read-code",
				Title:    "Explain split service boundaries",
				Prompt:   "Read the repository and explain the responsibility boundary between gateway, worker, and observability. Mention the concrete packages or files you used as evidence.",
				Graders: []Grader{
					{Type: "contains", Value: "gateway"},
					{Type: "contains", Value: "worker"},
					{Type: "contains", Value: "observability"},
				},
			},
			{
				ID:       "debug-test-sandbox-failure",
				Category: "debug",
				Title:    "Diagnose httptest sandbox failure",
				Prompt:   "A test run fails with `httptest: failed to listen on a port: listen tcp6 [::1]:0: bind: operation not permitted` in packages/llm/anthropic_response_test.go. Diagnose whether this points to an application regression or an execution environment issue, and say what evidence you would check.",
				Graders: []Grader{
					{Type: "contains", Value: "httptest"},
					{Type: "contains", Value: "environment"},
				},
			},
			{
				ID:       "architecture-benchmark-plan",
				Category: "complete-work",
				Title:    "Propose benchmark harness",
				Prompt:   "Propose a concrete benchmark harness for evaluating this agent on code reading, debugging, and task completion. Include what should be isolated from Slack/Postgres/Redis noise.",
				Graders: []Grader{
					{Type: "contains", Value: "agent"},
					{Type: "contains", Value: "Slack"},
					{Type: "contains", Value: "Postgres"},
				},
			},
		},
	}
}
