package llm

// NormalizingClient wraps an LLM client and normalizes assistant messages after each Chat.
type NormalizingClient struct {
	Inner Client
	Caps  Capabilities
}

func (c *NormalizingClient) Chat(ctx Context, req Request) (Response, error) {
	resp, err := c.Inner.Chat(ctx, req)
	if err != nil {
		return resp, err
	}
	resp.Message = NormalizeAssistantMessage(c.Caps, resp.Message, req.Tools)
	return resp, nil
}

// WrapClient returns a client that applies provider-aware message normalization.
func WrapClient(inner Client, caps Capabilities) Client {
	if inner == nil {
		return nil
	}
	return &NormalizingClient{Inner: inner, Caps: caps}
}
