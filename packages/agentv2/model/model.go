// Package model defines the provider-neutral model contract used by agent v2.
package model

import (
	"context"
	"encoding/json"
	"errors"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ContentType string

const (
	ContentText       ContentType = "text"
	ContentImage      ContentType = "image"
	ContentReasoning  ContentType = "reasoning"
	ContentJSON       ContentType = "json"
	ContentToolCall   ContentType = "tool_call"
	ContentToolResult ContentType = "tool_result"
	ContentArtifact   ContentType = "artifact"
)

// Content is a discriminated message payload. Only fields belonging to Type
// are populated. The canonical transcript therefore stays provider-neutral.
type Content struct {
	Type       ContentType     `json:"type"`
	Text       string          `json:"text,omitempty"`
	JSON       json.RawMessage `json:"json,omitempty"`
	ToolCall   *ToolCall       `json:"tool_call,omitempty"`
	ToolResult *ToolResult     `json:"tool_result,omitempty"`
	Artifact   *Artifact       `json:"artifact,omitempty"`
	ImageURL   string          `json:"image_url,omitempty"`
	Citations  []Citation      `json:"citations,omitempty"`
}

// Citation is structured evidence attached to the exact content block that
// uses it. Presentation surfaces decide how to render it; prompts only control
// citation style, never reconstruct source provenance from prose.
type Citation struct {
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	StartIndex int    `json:"start_index,omitempty"`
	EndIndex   int    `json:"end_index,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolResult struct {
	CallID  string    `json:"call_id"`
	Name    string    `json:"name,omitempty"`
	Content []Content `json:"content,omitempty"`
	IsError bool      `json:"is_error,omitempty"`
}

type Artifact struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	URI       string `json:"uri"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
}

type Message struct {
	ID      string    `json:"id,omitempty"`
	Role    Role      `json:"role"`
	Content []Content `json:"content"`
}

func TextMessage(role Role, value string) Message {
	return Message{Role: role, Content: []Content{{Type: ContentText, Text: value}}}
}

func (m Message) Text() string {
	var value string
	for _, block := range m.Content {
		if block.Type == ContentText {
			value += block.Text
		}
	}
	return value
}

func (m Message) Citations() []Citation {
	var citations []Citation
	for _, block := range m.Content {
		citations = append(citations, block.Citations...)
	}
	return citations
}

func (m Message) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, block := range m.Content {
		if block.Type == ContentToolCall && block.ToolCall != nil {
			calls = append(calls, *block.ToolCall)
		}
	}
	return calls
}

type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type Request struct {
	Model           string            `json:"model"`
	Messages        []Message         `json:"messages"`
	Tools           []ToolDefinition  `json:"tools,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	MaxOutputTokens int               `json:"max_output_tokens,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type FinishReason string

const (
	FinishStop      FinishReason = "stop"
	FinishToolCalls FinishReason = "tool_calls"
	FinishLength    FinishReason = "length"
	FinishCanceled  FinishReason = "canceled"
	FinishContent   FinishReason = "content_filter"
	FinishError     FinishReason = "error"
)

type Usage struct {
	InputTokens        int64 `json:"input_tokens,omitempty"`
	OutputTokens       int64 `json:"output_tokens,omitempty"`
	CacheReadTokens    int64 `json:"cache_read_tokens,omitempty"`
	CacheCreatedTokens int64 `json:"cache_created_tokens,omitempty"`
}

type Response struct {
	ID           string          `json:"id,omitempty"`
	Message      Message         `json:"message"`
	FinishReason FinishReason    `json:"finish_reason"`
	Usage        Usage           `json:"usage,omitempty"`
	RawMetadata  json.RawMessage `json:"raw_metadata,omitempty"`
}

type StreamEventType string

const (
	StreamStarted        StreamEventType = "started"
	StreamTextDelta      StreamEventType = "text_delta"
	StreamReasoningDelta StreamEventType = "reasoning_delta"
	StreamToolCallStart  StreamEventType = "tool_call_start"
	StreamToolArgsDelta  StreamEventType = "tool_arguments_delta"
	StreamToolCallDone   StreamEventType = "tool_call_completed"
	StreamUsage          StreamEventType = "usage"
	StreamCompleted      StreamEventType = "completed"
)

type StreamEvent struct {
	Type       StreamEventType `json:"type"`
	ResponseID string          `json:"response_id,omitempty"`
	Text       string          `json:"text,omitempty"`
	ToolCall   *ToolCall       `json:"tool_call,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}

type EventSink func(StreamEvent) error

// Client converts a provider wire protocol into canonical model events.
type Client interface {
	Generate(ctx context.Context, request Request, sink EventSink) (Response, error)
}

type ErrorKind string

const (
	ErrorTransient    ErrorKind = "transient"
	ErrorRateLimited  ErrorKind = "rate_limited"
	ErrorContextLimit ErrorKind = "context_limit"
	ErrorInvalid      ErrorKind = "invalid_request"
	ErrorAuth         ErrorKind = "authentication"
	ErrorUnavailable  ErrorKind = "unavailable"
	ErrorUnknown      ErrorKind = "unknown"
)

type Error struct {
	Kind       ErrorKind `json:"kind"`
	Message    string    `json:"message"`
	Retryable  bool      `json:"retryable"`
	StatusCode int       `json:"status_code,omitempty"`
	Cause      error     `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

func ErrorKindOf(err error) ErrorKind {
	var modelErr *Error
	if errors.As(err, &modelErr) {
		return modelErr.Kind
	}
	return ErrorUnknown
}
