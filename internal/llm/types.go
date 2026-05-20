package llm

import (
	"encoding/json"
)

type Message struct {
	Role             string `json:"role"`
	Content          string `json:"-"`
	ContentParts     []ContentPart
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Name             string     `json:"name,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

func TextPart(text string) ContentPart {
	return ContentPart{Type: "text", Text: text}
}

func ImageURLPart(url string) ContentPart {
	return ContentPart{Type: "image_url", ImageURL: &ImageURL{URL: url}}
}

func (m Message) MarshalJSON() ([]byte, error) {
	out := struct {
		Role             string     `json:"role"`
		Content          any        `json:"content,omitempty"`
		ReasoningContent string     `json:"reasoning_content,omitempty"`
		Name             string     `json:"name,omitempty"`
		ToolCallID       string     `json:"tool_call_id,omitempty"`
		ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	}{
		Role:             m.Role,
		ReasoningContent: m.ReasoningContent,
		Name:             m.Name,
		ToolCallID:       m.ToolCallID,
		ToolCalls:        m.ToolCalls,
	}
	if len(m.ContentParts) > 0 {
		out.Content = m.ContentParts
	} else if m.Content != "" {
		out.Content = m.Content
	}
	return json.Marshal(out)
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content,omitempty"`
		Name             string          `json:"name,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
		ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ReasoningContent = raw.ReasoningContent
	m.Name = raw.Name
	m.ToolCallID = raw.ToolCallID
	m.ToolCalls = raw.ToolCalls
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw.Content, &text); err == nil {
		m.Content = text
		return nil
	}
	var parts []ContentPart
	if err := json.Unmarshal(raw.Content, &parts); err == nil {
		m.ContentParts = parts
		for _, part := range parts {
			if part.Type == "text" && part.Text != "" {
				if m.Content != "" {
					m.Content += "\n"
				}
				m.Content += part.Text
			}
		}
		return nil
	}
	return nil
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolSpec struct {
	Type     string           `json:"type"`
	Function ToolSpecFunction `json:"function"`
}

type ToolSpecFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type Request struct {
	Model       string
	Messages    []Message
	Tools       []ToolSpec
	MaxTokens   int
	Temperature float64
	Thinking    string
}

type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
	Raw          json.RawMessage
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Client interface {
	Chat(ctx Context, req Request) (Response, error)
}

type Context interface {
	Done() <-chan struct{}
	Err() error
}
