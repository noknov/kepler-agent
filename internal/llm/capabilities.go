package llm

// Capabilities describes how a provider/protocol pair handles tool calls.
type Capabilities struct {
	Provider string
	Protocol string
	// NativeToolCalls is true when the transport exposes structured tool_use / tool_calls.
	NativeToolCalls bool
	// RepairTextualToolCalls enables runner-side detection and retry when the model
	// emits tool invocations as plain text instead of structured fields.
	RepairTextualToolCalls bool
}

// CapabilitiesFor returns default capabilities for a configured provider/protocol pair.
func CapabilitiesFor(provider, protocol string) Capabilities {
	provider = normalizeProviderName(provider)
	protocol = normalizeProtocolName(protocol)
	caps := Capabilities{
		Provider:               provider,
		Protocol:               protocol,
		NativeToolCalls:        true,
		RepairTextualToolCalls: true,
	}
	return caps
}

func normalizeProviderName(provider string) string {
	switch provider {
	case "mimo", "anthropic", "kimi", "moonshot", "opencode-go", "openai":
		return provider
	default:
		if provider == "" {
			return "unknown"
		}
		return provider
	}
}

func normalizeProtocolName(protocol string) string {
	switch protocol {
	case "anthropic", "openai":
		return protocol
	default:
		if protocol == "" {
			return "unknown"
		}
		return protocol
	}
}
