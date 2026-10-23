package runtime

import (
	"fmt"
	"github.com/noknov/kepler-agent/packages/agent/model"
)

// ContextPolicy versions the default tradeoffs. Zero fields select defaults;
// this is an operator/embedding contract, not model-authored configuration.
type ContextPolicy struct {
	Version          int `json:"version"`
	SoftLimitPercent int `json:"soft_limit_percent"`
	MaskMinBytes     int `json:"mask_min_bytes"`
	PreviewBytes     int `json:"preview_bytes"`
	RecentExchanges  int `json:"recent_exchanges"`
	ImageTokens      int `json:"image_tokens"`
}

func (p ContextPolicy) WithDefaults() ContextPolicy {
	if p.Version == 0 {
		p.Version = 1
	}
	if p.SoftLimitPercent == 0 {
		p.SoftLimitPercent = 85
	}
	if p.MaskMinBytes == 0 {
		p.MaskMinBytes = 2400
	}
	if p.PreviewBytes == 0 {
		p.PreviewBytes = 400
	}
	if p.RecentExchanges == 0 {
		p.RecentExchanges = 2
	}
	if p.ImageTokens == 0 {
		p.ImageTokens = 1024
	}
	return p
}
func (p ContextPolicy) Validate() error {
	p = p.WithDefaults()
	if p.Version != 1 || p.SoftLimitPercent < 1 || p.SoftLimitPercent > 100 || p.MaskMinBytes < 1 || p.PreviewBytes < 1 || p.PreviewBytes >= p.MaskMinBytes || p.RecentExchanges < 1 || p.ImageTokens < 1 {
		return fmt.Errorf("invalid context policy version or bounds")
	}
	return nil
}

// TokenCounter can be replaced by a provider tokenizer. It must count the
// complete supplied messages/schemas, including opaque continuation blocks.
// The built-in counter is an estimate and never claims exact provider parity.
type TokenCounter interface {
	Messages([]model.Message) int
	Tools([]model.ToolDefinition) int
}
type EstimatedCounter struct{ ImageTokens int }

func (c EstimatedCounter) Messages(messages []model.Message) int {
	cost := c.ImageTokens
	if cost <= 0 {
		cost = 1024
	}
	return estimateTokens(messages, cost)
}
func (EstimatedCounter) Tools(tools []model.ToolDefinition) int { return EstimateToolTokens(tools) }
func (c ContextConfig) counter() TokenCounter {
	if c.Counter != nil {
		return c.Counter
	}
	return EstimatedCounter{ImageTokens: c.Policy.WithDefaults().ImageTokens}
}

func hasNativeContinuation(message model.Message) bool {
	for _, block := range message.Content {
		if block.Type == model.ContentContinuation && block.Continuation != nil {
			return true
		}
	}
	return false
}
