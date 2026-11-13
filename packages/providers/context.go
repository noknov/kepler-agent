package providers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/noknov/kepler-agent/packages/agent/model"
)

func (c *Client) inputContextKey(modelName string) string {
	if c == nil || c.route == "" {
		return ""
	}
	native := false
	if routed, ok := c.Wire.(interface{ AcceptsNativeContinuation(string) bool }); ok {
		native = routed.AcceptsNativeContinuation(modelName)
	}
	raw, _ := json.Marshal([]any{"input-v1", c.route, modelName, native})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (c *Client) InputContextKey(ctx context.Context, modelName string) (string, error) {
	if c == nil {
		return "", nil
	}
	if c.Host != nil {
		return model.InputContextKey(ctx, c.Host, modelName)
	}
	if c.chatgptPath != "" {
		bound, _, err := c.bindSession(ctx)
		if err != nil {
			return "", err
		}
		return bound.inputContextKey(modelName), nil
	}
	return c.inputContextKey(modelName), nil
}
