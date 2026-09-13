# Long-term user memory design

## Current state

Kepler has durable session transcripts and model-generated compaction summaries.
They preserve one conversation after a restart, but they are keyed by session and
are not retrieved for a different session. `user_prompt_assets` stores explicit
rules and skills by Slack user ID. Those assets are durable preferences, not an
agent-maintained memory system.

The two mechanisms must stay separate. Rules are user-authored instructions with
an explicit management UI. Memories are derived, fallible data with provenance,
confidence, temporal validity, and deletion semantics. Promoting generated
memories to the rule layer would let a mistaken extraction become a persistent
instruction.

## Proposed lifecycle

Use three bounded tiers:

1. **Working context** is the current transcript projection and its compaction.
2. **Episode records** hold direct user statements and completed-task outcomes
   with source session, turn, timestamp, and a short evidence excerpt.
3. **Consolidated memories** hold stable user preferences, profile facts, project
   state, and reusable procedures. Every record points back to one or more
   episodes.

Writing and retrieval are separate paths. A response must not wait for background
consolidation, and a failed memory write must never lose the canonical transcript.
The transcript is the recovery source for rebuilding derived memories.

### Write triggers

- An explicit request such as “remember this” creates a high-confidence candidate
  immediately and reports what was stored.
- A direct user statement may create a candidate after the turn when it describes
  a stable preference or durable fact. Tool output, quoted text, retrieved pages,
  and assistant guesses are never eligible sources.
- Task completion may create an episodic outcome, but only reusable facts are
  consolidated. Raw chain-of-thought and credentials are never stored.
- Periodic consolidation runs off the request path after enough new episodes or
  when duplicate/conflicting candidates accumulate. Time alone is not a reason to
  manufacture a memory.

The writer emits one of `ADD`, `UPDATE`, `SUPERSEDE`, `DELETE`, or `NOOP` against a
stable semantic key. Automatic writes require evidence, a confidence threshold,
and a policy filter. Sensitive traits and secrets require explicit opt-in; short
lived values receive `valid_until` rather than becoming permanent.

### Storage contract

Each record should include:

```text
id, slack_team_id, slack_user_id, scope, kind, semantic_key, content
source_session_id, source_turn_id, source_event_sequence, evidence_hash
confidence, valid_from, valid_until, last_confirmed_at
status, supersedes_id, created_at, updated_at, version
embedding_model, embedding_version, embedding
```

The identity must include the Slack workspace and user, not only a globally
ambiguous user ID. All reads, updates, and deletes must include that pair in the SQL
predicate. Use optimistic `version` checks for consolidation races. Keep tombstones
long enough to stop replay from recreating deliberately forgotten data, then apply
the product retention policy.

### Retrieval

Retrieve with a fixed latency and token budget:

1. Filter by complete user identity, active status, scope, and temporal validity.
2. Generate candidates with lexical search, semantic vectors, recency, and exact
   semantic-key matches.
3. Rerank for query relevance, confidence, freshness, and source quality. Penalize
   contradictions and repeatedly retrieved memories that were not useful.
4. Inject only the top records inside an explicitly untrusted memory boundary.
   Include dates when a fact can change.

Do not inject the complete memory table into every turn. Cache embeddings and
candidate IDs, not final rankings; invalidate them after update, delete, or
supersede. If retrieval fails, continue without memory and emit a metric rather
than silently pretending that the user has no memories.

## Accuracy and user control

- Show a “why this was remembered” source and offer list, edit, forget, and disable
  controls. A user correction supersedes the old record in one transaction.
- Keep extracted facts atomic. “Uses Go, works on project X, prefers short replies”
  is three memories with different lifetimes and scopes.
- Preserve event time and observation time separately. Retrieval must answer
  temporal questions using the state valid at the requested time.
- Never turn absence of evidence into a negative fact. Never infer protected or
  sensitive attributes.
- Sample automatic writes for review and track precision. Favor `NOOP` when the
  evidence is ambiguous; a missing memory is less damaging than a confident false
  one that changes an external action.

## Evaluation plan

The existing local coding runner starts every case with a fresh workspace and
HOME, so it cannot measure cross-session memory. Memory evaluation needs a stateful
hosted harness with an isolated identity and database namespace per case.

Every release suite should cover:

- explicit remember, list, correction, forget, disable, and deletion persistence;
- cross-session recall, paraphrased cues, temporal updates, contradictions, and
  multi-hop relationships;
- distractors, quoted prompt injection, malicious tool output, cross-user and
  cross-tenant isolation, and a deleted-memory replay attempt;
- action grounding: whether the agent applies a remembered constraint to tool
  selection and parameters, rather than merely answering a recall question;
- false-memory rate, unsupported-write rate, retrieval precision/recall, answer
  attribution, stale-memory use, deletion success, p50/p95 retrieval latency,
  tokens injected, write amplification, and storage growth.

Use fixed seeds but at least three repetitions for model-scored cases. Keep a
human-reviewed gold set and adversarial holdout set. Gates should require zero
cross-user leaks and zero post-deletion recalls, enforce a false-write ceiling,
and compare task success and latency against a compatible no-memory baseline.
Do not use a single LLM judge as the only oracle: deterministic state assertions
and tool-argument checks take priority, with blinded pairwise judges only for
semantic response quality.

Useful research starting points are [Generative Agents (UIST 2023)](https://doi.org/10.1145/3586183.3606763)
for observation/reflection/retrieval, [LongMem](https://arxiv.org/abs/2306.07174)
and [MemGPT](https://arxiv.org/abs/2310.08560) for tiered bounded context, and
[LoCoMo (ACL 2024)](https://aclanthology.org/2024.acl-long.747/) for long-horizon
temporal evaluation. [Mem2ActBench (ACL 2026)](https://aclanthology.org/2026.acl-long.370/)
is especially relevant to Kepler because it tests whether memory changes tool use
and argument grounding instead of testing isolated fact recall only.

## Delivery order

1. Add the identity-safe schema, CRUD API, user controls, metrics, and deterministic
   explicit-memory tests.
2. Add bounded hybrid retrieval and prompt boundaries behind a per-user feature
   flag. Run shadow retrieval without injecting results first.
3. Add asynchronous candidate extraction and consolidation, initially review-only.
4. Enable high-confidence automatic writes for a cohort only after the precision,
   isolation, deletion, action-grounding, latency, and cost gates pass.
