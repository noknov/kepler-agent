package model

import "context"

// ContextIdentity names the actual provider/account/model rendering route, not
// credentials or an endpoint guessed by the runtime. Unknown adapters opt out.
// The key is evidence for token estimation; it never grants access.
type ContextIdentity interface {
	InputContextKey(context.Context, string) (string, error)
}

func InputContextKey(ctx context.Context, client Client, modelName string) (string, error) {
	if identity, ok := client.(ContextIdentity); ok {
		return identity.InputContextKey(ctx, modelName)
	}
	return "", nil
}

func (c *governedClient) InputContextKey(ctx context.Context, modelName string) (string, error) {
	return InputContextKey(ctx, c.Client, modelName)
}

func (c *ResilientClient) InputContextKey(ctx context.Context, modelName string) (string, error) {
	if c == nil {
		return "", nil
	}
	// A fallback's measurement cannot become evidence for the primary route.
	return InputContextKey(ctx, c.Primary, modelName)
}
