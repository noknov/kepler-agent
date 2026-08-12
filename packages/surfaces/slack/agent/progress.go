package slackagent

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/noknov/slack-copilot-agent/packages/agent/model"
	"github.com/noknov/slack-copilot-agent/packages/surfaces/slack/conversation"
)

// ProgressSummarizer turns confirmed tool intent into presentation text. It
// owns no runtime state and its output never enters the canonical transcript.
type ProgressSummarizer struct {
	Client           model.Client
	Model            string
	Timeout          time.Duration
	Sanitize         func(string) string
	ToolDescriptions map[string]string
}

func (p *ProgressSummarizer) Summarize(ctx context.Context, request string, calls []model.ToolCall, cjk bool) (string, error) {
	if p == nil || p.Client == nil || len(calls) == 0 {
		return "", nil
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := p.Client.Generate(ctx, model.Request{
		Model: p.Model, Messages: []model.Message{
			model.TextMessage(model.RoleSystem, progressSystemPrompt(cjk)),
			model.TextMessage(model.RoleUser, progressPromptWithDescriptions(request, calls, p.Sanitize, p.ToolDescriptions)),
		}, ReasoningEffort: "disabled", MaxOutputTokens: 64,
	}, nil)
	if err != nil {
		return "", err
	}
	text := decodeProgress(response.Message.Text(), cjk)
	if p.Sanitize != nil {
		text = p.Sanitize(text)
	}
	return text, nil
}

func (s *slackStream) ToolStep(calls []model.ToolCall) {
	if s.status == nil || s.progress == nil {
		return
	}
	s.mu.Lock()
	if s.progressSeen == nil {
		s.progressSeen = make(map[string]bool)
	}
	pending := make([]model.ToolCall, 0, len(calls))
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id != "" {
			if s.progressSeen[id] {
				continue
			}
			s.progressSeen[id] = true
		}
		pending = append(pending, call)
	}
	if len(pending) == 0 {
		s.mu.Unlock()
		return
	}
	s.statusEpoch++
	epoch := s.statusEpoch
	s.mu.Unlock()
	cjk := slackconversation.IsCJK(s.req.Text)
	go func() {
		text, err := s.progress.Summarize(s.ctx, s.req.Text, pending, cjk)
		if err != nil || text == "" {
			return
		}
		status := "is thinking"
		if cjk {
			status = "正在思考"
		}
		s.setProgressStatus(epoch, status, text)
	}()
}

func progressSystemPrompt(cjk bool) string {
	if cjk {
		return `根据用户请求和即将执行的工具调用生成面向用户的当前操作状态。描述正在进行的证据收集或执行动作，不要复述用户的最终任务、意图或预期结论。工具描述是主要语义来源；用户请求和参数值只用于确定具体对象。工具名、函数名、参数名和系统标识符只是内部证据，不能作为输出词汇。证据不足时使用概括对象，不要编造未发生的结果。只返回 JSON：{"action":"简短动词","target":"具体对象"}。不加主语、计划、解释、标点。`
	}
	return `Generate a user-facing current-operation status from the user request and the tool calls about to run. Describe the evidence-gathering or execution action currently underway; do not restate the user's final task, intent, or expected conclusion. Tool descriptions are the primary semantic source; use the user request and argument values only to identify the concrete object. Tool names, function names, argument keys, and system identifiers are internal evidence, not output vocabulary. When evidence is insufficient, use a general object instead of inventing an unobserved result. Return only JSON: {"action":"short present-participle verb","target":"concrete object"}. Add no subject, plan, explanation, or punctuation.`
}

type progressCall struct {
	Tool        string         `json:"tool"`
	Description string         `json:"description,omitempty"`
	Arguments   map[string]any `json:"arguments,omitempty"`
}

func progressPrompt(request string, calls []model.ToolCall, sanitize func(string) string) string {
	return progressPromptWithDescriptions(request, calls, sanitize, nil)
}

func progressPromptWithDescriptions(request string, calls []model.ToolCall, sanitize func(string) string, descriptions map[string]string) string {
	tools := make([]progressCall, 0, len(calls))
	for _, call := range calls {
		if name := strings.TrimSpace(call.Name); name != "" {
			description := strings.TrimSpace(descriptions[name])
			if sanitize != nil {
				description = sanitize(description)
			}
			tools = append(tools, progressCall{
				Tool:        name,
				Description: truncateProgressString(description, 240),
				Arguments:   progressArguments(call.Arguments, sanitize),
			})
		}
	}
	request = strings.TrimSpace(request)
	if sanitize != nil {
		request = sanitize(request)
	}
	data, _ := json.Marshal(struct {
		Request string         `json:"request"`
		Tools   []progressCall `json:"tools"`
	}{Request: request, Tools: tools})
	return string(data)
}

func progressArguments(raw json.RawMessage, sanitize func(string) string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	args, _ := progressArgumentValue(value, sanitize, 0).(map[string]any)
	if len(args) == 0 {
		return nil
	}
	return args
}

func progressArgumentValue(value any, sanitize func(string) string, depth int) any {
	if depth > 2 {
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			clean := progressArgumentValue(child, sanitize, depth+1)
			if clean != nil {
				out[key] = clean
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		limit := len(v)
		if limit > 8 {
			limit = 8
		}
		out := make([]any, 0, limit)
		for _, child := range v[:limit] {
			clean := progressArgumentValue(child, sanitize, depth+1)
			if clean != nil {
				out = append(out, clean)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return nil
		}
		if sanitize != nil {
			text = sanitize(text)
		}
		return truncateProgressString(text, 120)
	case json.Number, bool, nil:
		return v
	default:
		return nil
	}
}

func truncateProgressString(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func decodeProgress(text string, cjk bool) string {
	var label struct {
		Action string `json:"action"`
		Target string `json:"target"`
	}
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&label) != nil {
		return ""
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ""
	}
	label.Action = strings.TrimSpace(label.Action)
	label.Target = strings.TrimSpace(label.Target)
	if label.Action == "" || label.Target == "" || strings.ContainsAny(label.Action+label.Target, "\r\n") {
		return ""
	}
	if cjk {
		return label.Action + label.Target
	}
	return label.Action + " " + label.Target
}
