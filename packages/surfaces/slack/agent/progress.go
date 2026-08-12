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
	Client   model.Client
	Model    string
	Timeout  time.Duration
	Sanitize func(string) string
}

func (p *ProgressSummarizer) Summarize(ctx context.Context, request string, calls []model.ToolCall, cjk bool) (string, error) {
	if p == nil || p.Client == nil || len(calls) == 0 {
		return "", nil
	}
	if p.Sanitize != nil {
		request = p.Sanitize(request)
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
			model.TextMessage(model.RoleUser, progressPrompt(request, calls)),
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
		return `根据用户请求和即将执行的工具生成当前操作。只返回 JSON：{"action":"简短动词","target":"具体对象"}。不加主语、计划、解释、工具名、标点或未发生的结果。`
	}
	return `Generate the current operation from the user request and tools about to run. Return only JSON: {"action":"short present-participle verb","target":"concrete object"}. Add no subject, plan, explanation, tool name, punctuation, or unobserved result.`
}

func progressPrompt(request string, calls []model.ToolCall) string {
	tools := make([]string, 0, len(calls))
	for _, call := range calls {
		if name := strings.TrimSpace(call.Name); name != "" {
			tools = append(tools, name)
		}
	}
	data, _ := json.Marshal(struct {
		Request string   `json:"request"`
		Tools   []string `json:"tools"`
	}{Request: strings.TrimSpace(request), Tools: tools})
	return string(data)
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
