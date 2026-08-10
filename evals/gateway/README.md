# Controlled model gateway

The gateway provides one auditable model alias to every harness. LiteLLM exposes OpenAI Chat Completions at `/v1/chat/completions`, OpenAI Responses at `/v1/responses`, and Anthropic Messages-compatible requests at `/v1/messages`. Verify all three routes against the pinned image before a benchmark run: this project and Pi use Chat Completions in the example, Codex uses Responses, and Claude Code uses Messages.

```sh
docker compose -f evals/gateway/compose.yaml up -d
export EVAL_OPENAI_BASE_URL=http://127.0.0.1:4000/v1
export EVAL_ANTHROPIC_BASE_URL=http://127.0.0.1:4000
export OPENAI_API_KEY=$LITELLM_MASTER_KEY
export ANTHROPIC_API_KEY=$LITELLM_MASTER_KEY
```

Record the image digest, upstream model revision, gateway configuration, sampling parameters, and candidate versions with every result. The two protocol routes must map to `controlled-model`; otherwise the comparison is invalid.
