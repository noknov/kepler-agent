# Model configuration

[Documentation index](README.md) · [Configuration ownership](configuration.md)

These are configuration examples for adapters present in this source tree, not
a live catalog of model availability, pricing, or provider guarantees. Confirm
the endpoint, credentials, model ID, reasoning support, and output limits with
your deployed provider. Keep credentials in deploy secrets. The local CLI
receives its model configuration from the gateway.

`LLM_MAX_OUTPUT_TOKENS` sets the requested output cap when positive; zero or
unset leaves it to the provider. Budget context, tool definitions, output,
compaction, and retries together.

## LLM Providers

### LongCat

```bash
LLM_PROVIDER=longcat
LONGCAT_API_KEY=Bearer lc-...
LONGCAT_BASE_URL=https://api.longcat.chat/anthropic
LONGCAT_MODEL=LongCat-2.0
LONGCAT_PROTOCOL=anthropic
```

When using the Anthropic-compatible LongCat endpoint, the API key must include
the `Bearer ` prefix. For the OpenAI-compatible endpoint, use
`LONGCAT_BASE_URL=https://api.longcat.chat/openai`,
`LONGCAT_PROTOCOL=openai`, and omit the prefix.

### DeepSeek

```bash
LLM_PROVIDER=deepseek
DEEPSEEK_PROTOCOL=openai
DEEPSEEK_API_KEY=sk-...
DEEPSEEK_BASE_URL=https://api.deepseek.com
DEEPSEEK_MODEL=deepseek-v4-flash
```

### MiMo

```bash
LLM_PROVIDER=mimo
MIMO_PROTOCOL=anthropic
MIMO_API_KEY=...
MIMO_BASE_URL=https://token-plan-cn.xiaomimimo.com/anthropic
MIMO_MODEL=mimo-v2.5
MIMO_THINKING=disabled
```

MiMo thinking is disabled by default because multi-turn tool calls must preserve
provider-specific reasoning fields across turns.

### CLIProxyAPI

```bash
LLM_PROVIDER=cliproxyapi
CLIPROXYAPI_BASE_URL=http://127.0.0.1:8317/v1
CLIPROXYAPI_API_KEY=your-local-gateway-key
CLIPROXYAPI_MODEL=kimi/kimi-k2.7-code
```

Run and authenticate CLIProxyAPI locally first. It exposes OpenAI-compatible
endpoints and owns provider authentication separately.

### Kimi / Moonshot

Both names use the Moonshot OpenAI-compatible endpoint; choose the namespace
that matches the credential you operate.

```bash
LLM_PROVIDER=kimi
KIMI_API_KEY=...
KIMI_BASE_URL=https://api.moonshot.ai/v1
KIMI_MODEL=kimi-k2.6
```

```bash
LLM_PROVIDER=moonshot
MOONSHOT_API_KEY=...
MOONSHOT_BASE_URL=https://api.moonshot.ai/v1
MOONSHOT_MODEL=kimi-k2.6
```

### Anthropic

```bash
LLM_PROVIDER=anthropic
LLM_PROTOCOL=anthropic
LLM_ANTHROPIC_FLAVOR=official
ANTHROPIC_BASE_URL=https://api.anthropic.com
ANTHROPIC_API_KEY=sk-ant-...
ANTHROPIC_MODEL=claude-sonnet-4-5-20250929
```

### OpenAI-Compatible

```bash
LLM_PROVIDER=openai
OPENAI_API_KEY=...
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o-mini
```

Set `LLM_PROTOCOL=responses` for an OpenAI Responses endpoint. `openai`,
`responses`, and `anthropic` all adapt into the same canonical model contract;
the local CLI exposes the same choice as `--protocol`.

### OpenCode

OpenCode Zen and OpenCode Go use separate namespaces so free and subscription
credentials do not collide.

```bash
LLM_PROVIDER=opencode-zen
OPENCODE_ZEN_API_KEY=...
OPENCODE_ZEN_BASE_URL=https://opencode.ai/zen/v1
OPENCODE_ZEN_MODEL=mimo-v2.5-free
OPENCODE_ZEN_PROTOCOL=openai
```

```bash
LLM_PROVIDER=opencode-go
OPENCODE_GO_API_KEY=...
OPENCODE_GO_BASE_URL=https://opencode.ai/zen/go/v1
OPENCODE_GO_MODEL=glm-5.2
OPENCODE_GO_PROTOCOL=responses
```

Choose the protocol supported by the deployed endpoint and model, and verify
image inputs separately from text/tool requests. Leave temperature unset unless
the model accepts it and sampling control is intended; an explicit zero is
still a sent parameter. Compatibility can differ by model and endpoint.

## Secondary Model

The optional secondary model supports compact summaries, isolated exploration,
and Slack workflow classification. Hosted composition can also use it in the
primary/fallback chain; see [profile composition](../packages/profiles/hosted/profile.go).

```bash
SECONDARY_PROVIDER=opencode-zen
OPENCODE_ZEN_API_KEY=...
SECONDARY_MODEL=mimo-v2.5-free
```

When `SESSION_COMPACT_MODEL` is unset, compact summaries use `SECONDARY_MODEL`
when configured, otherwise the primary model.
