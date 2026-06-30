package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LongCatClient wraps AnthropicClient and adds support for LongCat's custom
// XML tool-call format:
//
//	<longcat_tool_call>tool-name
//	<longcat_arg_key>key</longcat_arg_key><longcat_arg_value>value</longcat_arg_value>
//	…
//	</longcat_tool_call>
//
// Standard Anthropic tool_use blocks take priority; XML parsing is a fallback
// for when the model embeds tool calls in the text stream instead.
type LongCatClient struct {
	inner *AnthropicClient
}

func NewLongCatClient(baseURL, apiKey string, timeout time.Duration) *LongCatClient {
	return &LongCatClient{
		inner: NewAnthropicClient(baseURL, apiKey, timeout, "official"),
	}
}

func (c *LongCatClient) Chat(ctx context.Context, req Request) (Response, error) {
	resp, err := c.inner.Chat(ctx, req)
	if err != nil {
		return resp, err
	}
	return enrichLongCatCalls(resp), nil
}

func (c *LongCatClient) ChatStream(ctx context.Context, req Request, h StreamHandler) (Response, error) {
	filt := &longcatStreamFilter{h: h}
	inner := StreamHandler{
		OnText:             func(delta string) { filt.feed(delta) },
		OnToolCallsStarted: h.OnToolCallsStarted,
		OnToolCallComplete: h.OnToolCallComplete,
		OnUsage:            h.OnUsage,
	}
	resp, err := c.inner.ChatStream(ctx, req, inner)
	filt.flush()
	if err == nil {
		resp = enrichLongCatCalls(resp)
	}
	return resp, err
}

// enrichLongCatCalls post-processes a response: if no standard tool_use blocks
// were returned, it parses any <longcat_tool_call> XML from the text content.
func enrichLongCatCalls(resp Response) Response {
	if len(resp.Message.ToolCalls) > 0 {
		return resp
	}
	calls := parseLongCatXML(resp.Message.Content)
	if len(calls) == 0 {
		return resp
	}
	resp.Message.Content = strings.TrimSpace(stripLongCatXML(resp.Message.Content))
	resp.Message.ToolCalls = calls
	resp.FinishReason = "tool_use"
	return resp
}

// ─── XML parsing ─────────────────────────────────────────────────────────────

const lcOpen  = "<longcat_tool_call>"
const lcClose = "</longcat_tool_call>"

var lcBlockRE = regexp.MustCompile(`(?s)<longcat_tool_call>(.*?)</longcat_tool_call>`)
var lcArgRE   = regexp.MustCompile(`(?s)<longcat_arg_key>(.*?)</longcat_arg_key>\s*<longcat_arg_value>(.*?)</longcat_arg_value>`)

func parseLongCatXML(text string) []ToolCall {
	blocks := lcBlockRE.FindAllStringSubmatch(text, -1)
	if len(blocks) == 0 {
		return nil
	}
	calls := make([]ToolCall, 0, len(blocks))
	for i, m := range blocks {
		inner := m[1]

		// First non-empty line is the tool name.
		name := ""
		for _, line := range strings.Split(inner, "\n") {
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "<") {
				name = t
				break
			}
		}
		if name == "" {
			continue
		}

		// Build a JSON object from key-value pairs.
		args := map[string]any{}
		for _, kv := range lcArgRE.FindAllStringSubmatch(inner, -1) {
			args[strings.TrimSpace(kv[1])] = coerceLCValue(strings.TrimSpace(kv[2]))
		}
		argsJSON, _ := json.Marshal(args)

		calls = append(calls, ToolCall{
			ID:   fmt.Sprintf("lc_%d", i),
			Type: "function",
			Function: ToolFunction{
				Name:      name,
				Arguments: string(argsJSON),
			},
		})
	}
	return calls
}

func stripLongCatXML(text string) string {
	return lcBlockRE.ReplaceAllString(text, "")
}

// coerceLCValue converts a string value to its most natural Go type.
func coerceLCValue(s string) any {
	switch strings.ToLower(s) {
	case "true":
		return true
	case "false":
		return false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

// ─── Streaming filter ─────────────────────────────────────────────────────────

// longcatStreamFilter intercepts text deltas from the underlying Anthropic
// client and suppresses <longcat_tool_call> XML blocks, forwarding only the
// human-readable narration text to the real handler.
//
// Tool calls are recovered from the complete response content after the stream
// ends (see enrichLongCatCalls).
type longcatStreamFilter struct {
	h       StreamHandler
	inXML   bool   // currently inside an XML block
	pending string // partial text that might be a tag prefix (or buffered XML)
}

func (f *longcatStreamFilter) feed(delta string) {
	if delta == "" {
		return
	}

	if f.inXML {
		f.pending += delta
		if idx := strings.Index(f.pending, lcClose); idx >= 0 {
			after := f.pending[idx+len(lcClose):]
			f.inXML = false
			f.pending = ""
			if after != "" {
				f.feed(after)
			}
		}
		return
	}

	text := f.pending + delta
	f.pending = ""

	for {
		idx := strings.Index(text, lcOpen)
		if idx >= 0 {
			if idx > 0 && f.h.OnText != nil {
				f.h.OnText(text[:idx])
			}
			f.inXML = true
			f.pending = text[idx:]
			// Close tag may already be present in the same chunk.
			if ci := strings.Index(f.pending, lcClose); ci >= 0 {
				after := f.pending[ci+len(lcClose):]
				f.inXML = false
				f.pending = ""
				text = after
				continue
			}
			return
		}

		// Check whether the tail is a partial prefix of the open tag.
		safeEnd := len(text)
		for i := len(lcOpen) - 1; i > 0; i-- {
			if strings.HasSuffix(text, lcOpen[:i]) {
				safeEnd = len(text) - i
				f.pending = text[safeEnd:]
				break
			}
		}
		if safeEnd > 0 && f.h.OnText != nil {
			f.h.OnText(text[:safeEnd])
		}
		return
	}
}

// flush emits any pending non-XML text that was held back as a potential tag prefix.
func (f *longcatStreamFilter) flush() {
	if f.pending != "" && !f.inXML && f.h.OnText != nil {
		f.h.OnText(f.pending)
		f.pending = ""
	}
}
