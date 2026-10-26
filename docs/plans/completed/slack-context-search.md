# Permission-scoped Slack search with source evidence

Status: completed. Baseline main005ec2ea; PR43 merged after exact-head CI152.

## Outcome and acceptance

Discover discussion evidence with user-initiated Slack Real-time Search. Return
source permalinks and explicit pagination; never substitute bot credentials,
persist action tokens or introduce another retrieval/vector database.

## Work

- [x] Retrieve one bounded assistant.search.context message page through the
  existing Slack HTTP client; validate inputs before credential lookup/network.
- [x] Reuse connected-user resolution, read/network policy, output budgets and
  connection-required behavior; register only in the hosted Slack catalog.
- [x] Consolidate duplicated client sources and credential-error conversion.
- [x] Declare user OAuth search scopes and document reconnect, eligible internal/
  published apps, interactive quotas and workspace semantic-search limitations.
- [x] Verify request/response shape, source/context retention, input/result bounds,
  typed API errors, scoped identity and absence of bot/other-user fallback.

## Decisions

Start with messages, relevance sorting and at most 20 results per explicit call.
No automatic cursor drain, history enrichment, query repair, legacy endpoint
fallback or local semantic approximation. Slack chooses retrieval mode and
enforces access. Existing runtime result budgets/artifacts own large evidence.

## Progress and evidence

Slack surface/connections/hosted-tool races, make check, merged-head broad checks,
isolated PostgreSQL/Redis integration, change-report and CI152 passed. Tests prove
invalid input does not resolve credentials, no other user's stored token is used,
and missing source identity cannot become successful retrieval evidence.

## Handoff

Configure search:read.public/private/im/mpim user scopes and reconnect existing
Slack connections before using RTS. Old tokens are not silently upgraded.
No live Slack search or measured semantic relevance/latency/cost claim. Lists and
Work Objects remain researched opportunities, not implemented features.
