# Permission-scoped Slack search with source evidence

Status: active. Baseline main005ec2ea (PR40 merged).

## Outcome and acceptance

Answer user-initiated cross-channel questions with targeted Slack Real-time
Search rather than exhaustively retrieving conversation histories. Return
message evidence, source permalinks and explicit pagination; never switch to a
bot token, persist action tokens or add a second retrieval/vector database.

## Work

- [x] Implement one bounded assistant.search.context page through the existing
  Slack client and a connected-user read tool; validate inputs before network.
- [x] Reuse existing connected-client resolution; preserve connection-required
  behavior and register only in the hosted Slack catalog with read/network policy.
- [x] Declare required OAuth search scopes and document reinstall/reconnect,
  internal/published app eligibility, workspace semantic-search limitations.
- [ ] Wire tests for exact request/response, limits, source/context retention,
  typed Slack errors, connection ownership and absence of bot fallback; affected
  races, make check, review and exact-head CI before merging.

## Decisions

Use the official RTS API, messages only initially: query + channel-type filters,
relevance sort, contextual messages and at most20 matches per explicit call.
Do not invent a local semantic reranker, keyword intent detector, automatic
search loop or legacy search.messages fallback. Slack determines semantic versus
keyword retrieval and enforces the user's permissions. Existing runtime result
budgets/artifacts own large evidence; explicit cursor lets the model selectively
request another page. Share existing client-source contract with conversation
reads, removing duplicated connected-client implementations.

## Progress and evidence

2026-10-27: reviewed official Slack context/RTS/method references. Current tool
catalog can read an identified conversation or file but has no workspace message
search. Connected-user tokens already live in the existing encrypted connection
store; RTS user-token calls do not require event action_token. No live workspace
access or semantic-quality claim.

## Handoff

Client/tool wiring implemented. make check and isolated PostgreSQL/Redis
make integration-check passed (including hosted connections/transcript and
Slack/event-inbox race suites). Slack surface, connection and hosted-tool race
suites passed, including explicit source identity/context retention, no bot or
other-user fallback, input bounds and connection-required behavior. Finish broad
review/publication. Source/deploy progress must be remote
before quota waits (<10%remaining; poll30minutes, no reset credit/manual resume).
No new schema, external dependency or production deployment planned.
