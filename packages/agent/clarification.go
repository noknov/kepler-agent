package agent

import (
	"encoding/json"
	"strings"
)

type clarificationErrorTracker struct {
	counts   map[string]int
	failures map[string][]clarificationFailure
}

type clarificationFailure struct {
	Tool  string
	Args  map[string]any
	Error string
}

func newClarificationErrorTracker() *clarificationErrorTracker {
	return &clarificationErrorTracker{
		counts:   map[string]int{},
		failures: map[string][]clarificationFailure{},
	}
}

func (t *clarificationErrorTracker) UserPrompt(results []toolResult, locale string) string {
	if t == nil {
		return ""
	}
	t.resetWithSuccessfulEvidence(results)
	for _, result := range results {
		if result.err == nil {
			continue
		}
		key := clarificationErrorKey(result)
		if key == "" {
			continue
		}
		t.counts[key]++
		t.failures[key] = append(t.failures[key], clarificationFailure{
			Tool:  result.name,
			Args:  decodeToolArgs(result.args),
			Error: conciseError(result.err.Error()),
		})
		if t.counts[key] >= clarificationErrorThreshold {
			return userFacingClarificationPrompt(locale, key)
		}
	}
	return ""
}

func (t *clarificationErrorTracker) resetWithSuccessfulEvidence(results []toolResult) {
	for _, result := range results {
		if result.err == nil {
			// Any successful tool call resets all counters — the model is
			// making progress and earlier failures were likely self-corrected.
			t.counts = map[string]int{}
			t.failures = map[string][]clarificationFailure{}
			return
		}
	}
}

func clarificationErrorKey(result toolResult) string {
	errText := strings.ToLower(result.err.Error())
	switch {
	// These are errors the model CANNOT self-correct — they require user input
	// or external action. Everything else (wrong path, wrong branch, wrong repo,
	// invalid params, timeouts) the model should retry with different parameters.
	case strings.Contains(errText, "unauthorized") ||
		strings.Contains(errText, "forbidden") ||
		strings.Contains(errText, "permission denied") ||
		strings.Contains(errText, "access denied") ||
		strings.Contains(errText, "authentication required"):
		return "auth_access"
	case strings.Contains(errText, "missing config") ||
		strings.Contains(errText, "not configured"):
		return "missing_config"
	default:
		return ""
	}
}

func decodeToolArgs(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil
	}
	return args
}

func conciseError(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > 180 {
		return string(runes[:180]) + "..."
	}
	return text
}

func userFacingClarificationPrompt(locale, key string) string {
	zh := strings.HasPrefix(strings.ToLower(locale), "zh")
	switch key {
	case "auth_access":
		if zh {
			return "我没有权限访问这个资源，需要额外授权才能继续。"
		}
		return "I don't have permission to access this resource. It needs additional authorization to proceed."
	case "missing_config":
		if zh {
			return "缺少必要的配置，暂时无法执行这个操作。"
		}
		return "A required configuration is missing — I can't perform this operation right now."
	default:
		if zh {
			return "遇到了无法自动解决的问题，需要你的帮助。"
		}
		return "I've hit an issue I can't resolve automatically and need your help."
	}
}
