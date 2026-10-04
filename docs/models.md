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
DEEPSEEK_MODEL=deepseek-flash
DEEPSEEK_THINKING=low
DEEPSEEK_TIMEOUT=10m
```

Current DeepSeek model IDs are `deepseek-flash` (DeepSeek-V4.1-Flash) and
`deepseek-v4-pro`. `deepseek-flash` accepts image input, tool calls, and
thinking mode; `deepseek-v4-pro` has stronger long-context reasoning but no
image input. The legacy IDs `deepseek-v4-flash` and
`deepseek-v4-flash-vision-exp` still resolve to V4.1-Flash.

`DEEPSEEK_THINKING` maps to DeepSeek's `thinking` object and accepts
`disabled`, `enabled`, or a reasoning effort (`low`, `high`, `max`; `medium`
and `xhigh` collapse to `high`, `minimal` to `low`). Reasoning tokens share the
`LLM_MAX_OUTPUT_TOKENS` budget with the final answer, so a low effort keeps the
cap meaningful. DeepSeek caches prefixes on disk automatically; cache reads are
read from `prompt_cache_hit_tokens` (or `prompt_tokens_details.cached_tokens`)
and bill at the cache-hit rate. Peak hours are Beijing time Mon-Fri
09:00-12:00 and 14:00-18:00; off-peak requests bill at half price.

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

### ChatGPT subscription (operator session)

The operator signs in once on the local host. Hosted agent and gateway inference
use that protected session for all requests; downstream Kepler users do not
receive OAuth tokens or need to sign in to ChatGPT. This implements the official
[Sign in with ChatGPT OSS flow](https://developers.openai.com/siwc/token-sharing-open-source/sign-in)
and public Responses endpoint, not ChatGPT backend endpoints or CLIProxyAPI.
Account eligibility, plan usage limits, and permission to serve other users are
controlled by OpenAI; local deployment alone does not establish that permission.

```bash
go run ./cli/cmd/kepler-agent chatgpt login --credentials-file /private/operator-chatgpt/session.json
go run ./cli/cmd/kepler-agent chatgpt models --credentials-file /private/operator-chatgpt/session.json
```

Open the printed URL in your browser and approve ChatGPT plan usage. Choose an
actual model slug printed by `models`, then configure the inference service:

```bash
LLM_PROVIDER=chatgpt
CHATGPT_CREDENTIALS_FILE=/private/operator-chatgpt/session.json
CHATGPT_MODEL=<model-slug-from-your-account>
# Optional: share this session with the Explorer/fallback model.
SECONDARY_PROVIDER=chatgpt
SECONDARY_MODEL=<model-slug-from-your-account>
```

No API key is needed. The `chatgpt` provider requires protocol `responses` and
`https://api.openai.com/v1`. Keep the credential directory outside agent workspace
and additional read roots. Session files use owner-only permissions and atomic
replacement. Mount the **directory** read/write in Docker (not just a file),
because refresh rotates tokens and replaces the file. Refreshes are serialized
with a file lock across processes sharing the same directory. Use one credential
store; independently copied stores must not concurrently refresh the same session.
The host running login and the inference service need file write access.

`chatgpt status` reports identity and access-token expiry without printing tokens.
Reauthorization reuses the registered client and verifies the original identity;
use a separate directory to register another account. Disconnect the app in
ChatGPT Settings to revoke access. Never commit the session or host credential files.

Plan requests always stream with `store=false`, omit unsupported sampling/output
parameters, use developer messages and namespaced function tools, and send full
conversation context. The runtime executes tools locally. Streams that fail,
exhaust plan usage, or end without completed inference return errors; the client
never silently switches to API billing. Runtime fallback, if configured, still
follows the configured secondary provider. These restrictions are specific to
ChatGPT plan usage; normal OpenAI API clients keep their existing encoding.

### Qoder subscription (operator PAT)

```bash
LLM_PROVIDER=qoder
QODER_MODEL=efficient
QODER_THINKING=disabled
# QODER_PERSONAL_ACCESS_TOKEN comes from worker SOPS secrets, not this file.
```

The native `qoder` protocol uses `https://api2.qoder.sh` by default. The optional
`QODER_BASE_URL` can select the official global `https://api3.qoder.sh` endpoint.
It is not an OpenAI-compatible endpoint. The adapter makes one model request;
Kepler continues to own tools, capability policy, transcripts, retries and
compaction. No Qoder CLI, SDK runtime or proxy process is required.

The operator command `kepler-agent qoder models` reads the PAT from the process
environment and prints enabled model keys and names from the account catalog.
Run it in an operator environment populated by the deploy secret renderer.
Choose a returned key or exact display name for `QODER_MODEL` or
`SECONDARY_MODEL`; there is no fallback
model list or inferred mapping from tiers to vendors. Catalogs are cached for
five minutes; a catalog refresh failure returns an error rather than using a
stale list. The factory supports Qoder as either primary or secondary provider.

The PAT is exchanged for a short-lived job token. That token and account identity
stay in process memory and are re-acquired before expiry. Redirects are rejected
and authentication/error response bodies are not logged. Replacing the PAT in
SOPS requires recreating the worker; no local credential file is maintained.

Inference uses Qoder's native COSY signature and encoded SSE protocol, based on
the vendor CLI protocol and the MIT-licensed
[Pi Qoder provider](https://github.com/simonsmh/pi-provider-qoder). This is an
account-tested protocol adapter, **not a documented standalone model API
guarantee**. Qoder protocol changes may require an adapter update. The official
[Agent SDK](https://docs.qoder.com/cli/sdk/how-it-works) owns its own agent loop
and is therefore not embedded in the Kepler model provider.

Text and structured tool-call round trips were verified using the `efficient`
model in the real Kepler runtime. Other models and image input need representative
validation before operational use. Native `reasoning_content` stays separate
from displayed text. Tool-only assistant turns retain blank content because the
native gateway otherwise drops them and loses the relation to tool results.

Streams require both a terminal finish reason and the native `[DONE]` marker.
Qoder's native text has no answer/commentary phase. The shared model adapter
waits for a validated complete response when an unphased request has tools:
tool-call steps emit accompanying prose as `commentary`, while the final answer
is emitted as `final_answer`. Classification uses tool-call structure, never
wording. Plain chat without tools and explicitly phased final answers retain
live streaming. First-token arrival is reported independently, so deferred
presentation does not inflate latency metrics. Slack renders commentary as
temporary free-text assistant-thread status and answers as message text.
The last status is renewed while running to avoid Slack's two-minute expiry.
Unsupported session channels retain native processing status. Actual execution
plans remain separate from model commentary.
Malformed events, upstream errors inside HTTP 200, incomplete tool calls and
truncated streams fail explicitly. Token usage is reported only when token
fields are present; native Credits are retained in response metadata as `usage`
and are never substituted for tokens or USD. Existing ChatGPT configuration
remains the default until the operator changes `LLM_PROVIDER`.

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
OPENCODE_GO_MODEL=gpt-5.6-luna
OPENCODE_GO_PROTOCOL=openai
```

Known OpenCode Go models are resolved through the provider catalog: Grok 4.6
and GPT 5.6 Luna use Responses, while DeepSeek V4.1 Flash uses Chat
Completions and accepts image input. Add a verified model route to the provider
catalog before using a new model; its protocol and input modalities then apply
consistently to every surface. Leave temperature unset unless the model accepts
it and sampling control is intended; an explicit zero is still a sent parameter.

## Secondary Model

The optional secondary model supports compact summaries, isolated exploration,
and Slack workflow classification. Hosted composition can also use it in the
primary/fallback chain; see [profile composition](../packages/profiles/hosted/profile.go).

```bash
SECONDARY_PROVIDER=opencode-go
OPENCODE_GO_API_KEY=...
SECONDARY_MODEL=deepseek-v4.1-flash
```

When `SESSION_COMPACT_MODEL` is unset, compact summaries use `SECONDARY_MODEL`
when configured, otherwise the primary model.
