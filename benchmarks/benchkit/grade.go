package benchkit

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func Grade(ctx context.Context, c Case, result AgentResult) ([]CheckResult, bool, float64) {
	if len(c.Graders) == 0 {
		return nil, true, 1
	}
	checks := make([]CheckResult, 0, len(c.Graders))
	passed := 0
	for _, grader := range c.Graders {
		check := runGrader(ctx, c, result, grader)
		if check.Passed {
			passed++
		}
		checks = append(checks, check)
	}
	score := float64(passed) / float64(len(checks))
	return checks, passed == len(checks), score
}

func runGrader(ctx context.Context, c Case, result AgentResult, grader Grader) CheckResult {
	switch grader.Type {
	case "contains":
		ok := strings.Contains(strings.ToLower(result.Output), strings.ToLower(grader.Value))
		return CheckResult{Type: grader.Type, Target: grader.Value, Passed: ok, Message: containsMessage(ok, grader.Value)}
	case "not_contains":
		ok := !strings.Contains(strings.ToLower(result.Output), strings.ToLower(grader.Value))
		return CheckResult{Type: grader.Type, Target: grader.Value, Passed: ok, Message: notContainsMessage(ok, grader.Value)}
	case "equals":
		ok := strings.TrimSpace(result.Output) == strings.TrimSpace(grader.Value)
		return CheckResult{Type: grader.Type, Target: grader.Value, Passed: ok, Message: equalsMessage(ok)}
	case "regex":
		return gradeRegex(result.Output, grader)
	case "file_contains":
		return gradeFileContains(c, grader)
	case "file_not_contains":
		return gradeFileNotContains(c, grader)
	case "file_exists":
		return gradeFileExists(c, grader)
	case "command":
		return gradeCommand(ctx, c, grader)
	case "patch_contains":
		ok := strings.Contains(strings.ToLower(result.Patch), strings.ToLower(grader.Value))
		return CheckResult{Type: grader.Type, Target: grader.Value, Passed: ok, Message: containsMessage(ok, grader.Value)}
	case "patch_not_contains":
		ok := !strings.Contains(strings.ToLower(result.Patch), strings.ToLower(grader.Value))
		return CheckResult{Type: grader.Type, Target: grader.Value, Passed: ok, Message: notContainsMessage(ok, grader.Value)}
	case "tool_called":
		count := result.ToolCallCounts[grader.Value]
		return CheckResult{Type: grader.Type, Target: grader.Value, Passed: count > 0, Message: fmt.Sprintf("called %d times", count)}
	case "max_tool_calls":
		max, err := strconv.Atoi(strings.TrimSpace(grader.Value))
		if err != nil {
			return CheckResult{Type: grader.Type, Target: grader.Value, Passed: false, Message: "max_tool_calls value must be an integer"}
		}
		return CheckResult{Type: grader.Type, Target: grader.Value, Passed: result.ToolCalls <= max, Message: fmt.Sprintf("tool_calls=%d max=%d", result.ToolCalls, max)}
	default:
		return CheckResult{Type: grader.Type, Passed: false, Message: "unknown grader type"}
	}
}

func gradeRegex(output string, grader Grader) CheckResult {
	re, err := regexp.Compile(grader.Value)
	if err != nil {
		return CheckResult{Type: "regex", Target: grader.Value, Passed: false, Message: err.Error()}
	}
	ok := re.MatchString(output)
	return CheckResult{Type: "regex", Target: grader.Value, Passed: ok, Message: containsMessage(ok, grader.Value)}
}

func gradeFileContains(c Case, grader Grader) CheckResult {
	path := grader.Path
	if c.Workspace != "" && !filepath.IsAbs(path) {
		path = filepath.Join(c.Workspace, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckResult{Type: "file_contains", Target: grader.Path, Passed: false, Message: err.Error()}
	}
	ok := strings.Contains(string(data), grader.Value)
	return CheckResult{Type: "file_contains", Target: grader.Path, Passed: ok, Message: containsMessage(ok, grader.Value)}
}

func gradeFileNotContains(c Case, grader Grader) CheckResult {
	path := grader.Path
	if c.Workspace != "" && !filepath.IsAbs(path) {
		path = filepath.Join(c.Workspace, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CheckResult{Type: "file_not_contains", Target: grader.Path, Passed: false, Message: err.Error()}
	}
	ok := !strings.Contains(string(data), grader.Value)
	return CheckResult{Type: "file_not_contains", Target: grader.Path, Passed: ok, Message: notContainsMessage(ok, grader.Value)}
}

func gradeFileExists(c Case, grader Grader) CheckResult {
	path := grader.Path
	if c.Workspace != "" && !filepath.IsAbs(path) {
		path = filepath.Join(c.Workspace, path)
	}
	_, err := os.Stat(path)
	ok := err == nil
	message := "exists"
	if err != nil {
		message = err.Error()
	}
	return CheckResult{Type: "file_exists", Target: grader.Path, Passed: ok, Message: message}
}

func gradeCommand(ctx context.Context, c Case, grader Grader) CheckResult {
	if len(grader.Command) == 0 {
		return CheckResult{Type: "command", Passed: false, Message: "command grader requires command"}
	}
	timeout := time.Duration(grader.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, grader.Command[0], grader.Command[1:]...)
	if c.Workspace != "" {
		cmd.Dir = c.Workspace
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return CheckResult{Type: "command", Target: strings.Join(grader.Command, " "), Passed: false, Message: fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out)))}
	}
	return CheckResult{Type: "command", Target: strings.Join(grader.Command, " "), Passed: true, Message: strings.TrimSpace(string(out))}
}

func containsMessage(ok bool, value string) string {
	if ok {
		return "found " + value
	}
	return "missing " + value
}

func notContainsMessage(ok bool, value string) string {
	if ok {
		return "did not find " + value
	}
	return "unexpectedly found " + value
}

func equalsMessage(ok bool) string {
	if ok {
		return "matched expected output"
	}
	return "output did not match expected value"
}
