package benchkit

import "time"

type Suite struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Cases       []Case `json:"cases"`
}

type Case struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Title    string `json:"title,omitempty"`
	Prompt   string `json:"prompt"`
	// Workspace is an optional template directory. The runner copies it into a
	// per-case isolated evaluation workspace before invoking the agent.
	Workspace      string            `json:"workspace,omitempty"`
	Files          map[string]string `json:"files,omitempty"`
	Setup          []Command         `json:"setup,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Graders        []Grader          `json:"graders,omitempty"`
}

func (c Case) Timeout(defaultTimeout time.Duration) time.Duration {
	if c.TimeoutSeconds <= 0 {
		return defaultTimeout
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

type Grader struct {
	Type           string   `json:"type"`
	Value          string   `json:"value,omitempty"`
	Path           string   `json:"path,omitempty"`
	Command        []string `json:"command,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type Command struct {
	Argv           []string `json:"argv"`
	Workdir        string   `json:"workdir,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type AgentResult struct {
	Output            string         `json:"output"`
	Patch             string         `json:"patch,omitempty"`
	TerminationReason string         `json:"termination_reason,omitempty"`
	LLMCalls          int            `json:"llm_calls,omitempty"`
	ToolCalls         int            `json:"tool_calls,omitempty"`
	ToolCallCounts    map[string]int `json:"tool_call_counts,omitempty"`
}

type CaseResult struct {
	ID                string         `json:"id"`
	Category          string         `json:"category"`
	Title             string         `json:"title,omitempty"`
	Passed            bool           `json:"passed"`
	Score             float64        `json:"score"`
	DurationMillis    int64          `json:"duration_ms"`
	Workspace         string         `json:"workspace,omitempty"`
	Patch             string         `json:"patch,omitempty"`
	Output            string         `json:"output,omitempty"`
	Error             string         `json:"error,omitempty"`
	TerminationReason string         `json:"termination_reason,omitempty"`
	LLMCalls          int            `json:"llm_calls,omitempty"`
	ToolCalls         int            `json:"tool_calls,omitempty"`
	ToolCallCounts    map[string]int `json:"tool_call_counts,omitempty"`
	Checks            []CheckResult  `json:"checks,omitempty"`
}

type CheckResult struct {
	Type    string `json:"type"`
	Target  string `json:"target,omitempty"`
	Passed  bool   `json:"passed"`
	Message string `json:"message,omitempty"`
}

type Summary struct {
	Suite          string          `json:"suite"`
	Total          int             `json:"total"`
	Passed         int             `json:"passed"`
	Score          float64         `json:"score"`
	DurationMillis int64           `json:"duration_ms"`
	ByCategory     map[string]Stat `json:"by_category"`
}

type Stat struct {
	Total  int     `json:"total"`
	Passed int     `json:"passed"`
	Score  float64 `json:"score"`
}
