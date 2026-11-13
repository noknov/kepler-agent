# Hosted Slack

Slack receives and presents hosted turns. Tools execute in the worker's
operator-managed workspace, using hosted policy and configured credentials.
The integration expects reachable gateway and worker services plus PostgreSQL
and Redis.

## Connect the Slack app

Configure the app's event Request URL as:

```text
https://<public-origin>/slack/events
```

The gateway verifies signed requests. Configure `SLACK_SIGNING_SECRET`,
`SLACK_BOT_TOKEN`, `ALLOWED_SLACK_USERS`, and optionally
`ALLOWED_SLACK_CHANNELS` through the deploy configuration workflow.

The integration setup used by this project includes these scopes:

```text
app_mentions:read
channels:history  groups:history  im:history
chat:write        assistant:write
files:read
```

And these event subscriptions:

```text
app_mention  message.channels  message.groups  message.im
app_home_opened  app_context_changed  file_shared  reaction_added
agent_session_stopped
```

Use Slack's Agent experience (`agent_view`) for the corresponding presentation.
Enable only the features appropriate to your Slack app installation; event
access also depends on its permissions and conversation membership.

Slack JSON responses must contain one complete value within the existing 4 MiB
client budget. A valid prefix followed by another value, trailing data, or an
oversized body is not success. Ambiguous non-idempotent write responses retain
uncertain-delivery classification and are never automatically replayed; explicit
429 rejection still follows Retry-After. HTTP error diagnostics retain method
and status but omit raw upstream bodies, which may echo private content.

## Start a conversation

Mention the agent or use its supported direct-message entry. Replies continue
in the Slack thread under its ownership policy. User integration connections
are separate from Slack sign-in and may be requested before a tool can run.
See [tools and connections](tools.md).

App Home → Connections shows each saved integration instance. **Remove** asks
for confirmation, deletes only that user's selected instance and credentials,
and refreshes the list. It also works for unnamed or invalid connections; other
instances and users remain unchanged. This removes Kepler's local authorization,
not access granted in the provider's own account settings. Entries marked
`server credentials` are deployment configuration and have no personal Remove
action.

## Native task channels

Native Slack Code Channels are optional. Apply deployment migration 021 before
setting `SLACK_CODE_CHANNELS_ENABLED=true`. The app installation needs
`code_channels:manage`, conversation metadata read scopes, and the manifest's
`features.code_channels.enabled`. Availability also depends on Slack enabling
the feature for the app and initiating user. A successful local API contract
test does not establish workspace availability.

Explicitly allowlist `slack-create_task_space`, `slack-set_task_context`, and
`slack-set_task_view` in `AGENT_ALLOWED_WRITE_TOOLS` to permit these external
writes. Creation derives workspace, user and origin from the authenticated turn,
inherits origin privacy, and uses a stable Slack session key. It posts a concise
task brief. The active origin turn completes in place; the next explicit mention
in the new channel starts its separate canonical session. Original thread
history is not automatically copied. New channel grants remain bound to their
creator and authorized origin, even when another allowed user joins the channel.

A code channel is one session: replies, Stop, approvals, delivery recovery and
OAuth resumption omit thread timestamps. Classification uses Slack's native
record type and assigned agent, not names or empty timestamps. Existing native
channels without a Kepler grant require operator channel authorization. Channels
still require explicit mentions; DMs retain their existing direct-input policy.

The deferred `slack-list_task_views` tool lists Slack's view IDs, stable keys,
file IDs and versions without fetching every document. `slack-read_task_canvas`
reads an attached canvas and its anchored comments/replies; Slack's truncation
flags remain visible. Reads are scoped to the currently authorized native
channel, not a model-supplied destination.

Allowlist `slack-update_task_canvas` and `slack-remove_task_view` separately to
permit those writes. Canvas updates use Slack's section-aware Markdown API so
unchanged sections retain their comment anchors. It is not a conditional-write
API: neither a prior read nor user confirmation promises isolation from edits
made concurrently in Slack. Removing an HTML, diff or Block Kit view also
deletes its backing content file; removing a Canvas view detaches that view.
Both operations retain the existing requester confirmation policy.

Context bars replace up to five informational items. Views use stable keys;
diffs are channel singletons. HTML does not receive model-controlled CSP
extensions. Canvas attachments grant comment access. These are presentation
projections, not another task database or agent loop. Users can archive a task
channel through Slack's native UI; the agent does not archive its channel while
its final answer is still being delivered.

## Pull-request investigation

Ask the agent to inspect a PR with ordinary tools. There is no intent classifier,
workflow activation, mandatory reviewer team or sticky PR scope. Generic read-only
delegation is available when independent investigation helps. Each new request
provides its scope; previous review metadata does not select another execution path.

### Approve a pull request

`github-approve_pr` is an optional Slack-only external write. Configure the
credential owner's Slack ID as `GITHUB_APPROVAL_NOTIFY_USER_ID` and explicitly
allowlist `github-approve_pr` in `AGENT_ALLOWED_WRITE_TOOLS`. The worker uses its
existing `GITHUB_TOKEN`; no extra Slack-user or PR-author allowlist is installed.
Existing ingress access policy controls who can request work.

An allowed user must @mention the agent in a shared channel thread. After
inspecting the exact PR head and finding no obvious issues, the agent requests
confirmation in that same thread and cc's the credential owner. Only the
requesting user may confirm or cancel; cc is notification, not approval authority.
All private DMs are denied, including the credential owner's DM.

The write can only submit APPROVE. It cannot merge, comment or request changes.
The tool verifies the PR is open and its head still matches the reviewed SHA.
GitHub's own reviewer restrictions remain in force. Credential changes invalidate
pending approval contracts. Unverifiable write receipts remain unknown and are
not automatically replayed. No database migration is required.

## Delivery and recovery

PostgreSQL inbox claims, leases, retries, and deterministic message identities
support recovery. Delivery remains at least once across external systems;
Slack and PostgreSQL do not share a transaction. Do not infer that a request
failed solely because the visible answer is missing. Inspect the run and tool
result before repeating an external action.

For readiness, dead letters, shutdown, and run inspection, use
[operations](operations.md). For model and locale settings, use
[configuration](configuration.md).

DMs start or update an agent task directly, without an @mention. In channels and
channel threads, explicitly @mention the bot, including follow-ups,
pending-input answers and messages arriving while a task is running. Unmentioned
channel messages and standalone file-sharing notifications do not trigger
execution. Files can accompany a DM or an explicit channel mention.

## Native Stop and session status

Subscribe to `agent_session_stopped` in Slack App Settings → Event Subscriptions
→ Subscribe to bot events. Without this subscription Slack disables its native
Stop control; backend deployment alone cannot enable the button. The event uses
the existing signed `/slack/events` endpoint and access policy.

Stop commands persist in `agent_session_inputs` and target a specific turn, so
retries cannot cancel a later turn. Redis broadcasts a wake-up; each worker
checks the target against its running turn. Active workers also poll their
commands, recovering missed notifications without relying on a cached owner.
Inbox dispatch prioritizes Stop before paging the prompt backlog, and a reserved
worker handles it even when all prompt workers are occupied. Cancellation follows the normal runtime path,
which stops model/tool work, confirms cancellation and clears the session status.
Before deploying this change, apply `schema/postgres.sql` to allow the `stop`
session-input kind and add the dispatch index. Runtime code does not run DDL.

The terminal `active`/`suspended` status is sent after message delivery finishes:
Slack's stream APIs also change session status, so runtime completion alone is
too early. Posting an answer does not itself clear a non-streaming session.
`agents.sessions.setStatus` supports lifecycle values, not custom loading text.
Model commentary remains hidden; internal reasoning is not sent to Slack.

## Message-time viewing references

With `agent_view` and the `app_context_changed` subscription, Slack includes an
`app_context` snapshot in a DM message. Kepler preserves the first supported
same-workspace channel reference beside that message as canonical JSON data.
This enables requests such as “summarize the channel I am viewing” without
copying the full channel into every model request or tracking browsing activity.

The bot's reply channel, authenticated identity and tool scope stay bound to the
actual DM. The reference does not grant access: the agent must explicitly read
needed messages through the existing user-connected Slack tools. Unknown entity
types, malformed channel IDs and cross-workspace references are ignored. Empty
context means no reference; a later ambient context event cannot mutate an older
queued or steering message. User text remains unchanged for routing and recall.
No new cache, background fetch or database migration is involved.

Only the documented channel entity is supported here; canvas/thread/list entity
formats are not guessed. An absent snapshot never falls back to an older viewed
channel. Configure the required Slack app features before expecting references.
See [Slack viewing context](https://docs.slack.dev/ai/developing-agents/#tracking-what-the-user-is-viewing).

## Native suggested prompts

Opening the agent Messages tab offers supported starting points. When the Home
open event supplies a same-workspace viewed channel, two prompts explicitly name
that channel: a decision brief and extraction of stated next steps. They retain
source authors/timestamps and request owners/deadlines only when stated. Generic
PR review and attachment analysis remain available; without channel context the
page offers capability guidance instead of guessing a target.

These are presentation updates, not autonomous work. No task starts until the
user clicks/sends a suggestion. Suggested prompts do not grant Slack data access
or create/assign tasks. Text lives in the existing prompt catalog, with English
and Chinese defaults selected by SLACK_DEFAULT_LOCALE.

This uses `assistant.threads.setSuggestedPrompts` for agent_view without
`thread_ts`; Slack documents silent failure when that argument is supplied to an
agent app. Enable assistant:write and agent_view, and remove app-level static
suggested prompts before using dynamic suggestions. API/scope/configuration
failures are optional UI failures: they emit a bounded metric and content-free
error type, while ordinary messages remain usable. Access-denied users and
non-DM destinations receive no suggestions. No live Slack installation has been
modified or validated by the deterministic tests.

Conversation history and reply reads share cursor progression and remaining
message-budget enforcement. A repeated cursor is a protocol error, not another
page of successful evidence. Empty/short pages still follow next_cursor, as Slack
requires; they are not treated as proof that the conversation ended. User tools
and bootstrap history retain their existing positive message bounds. The raw
client's explicit Replies limit<=0 all-pages API still requires a caller-owned
cancellation/deadline when used outside those bounded surfaces.

User-directory lookup shares the same cursor progression guard as conversation
history. A cyclic users.list response fails before duplicate pages can become
ambiguous-person evidence; an empty page with a new cursor continues normally.


## Search workspace discussions

With Slack OAuth configured, `slack-user_search` uses the connected user's
`assistant.search.context` API to find message evidence across conversations.
For example: “What did we decide about the deployment rollback?” Results retain
source permalinks, channel/message/thread identity and surrounding messages.
Read a specific thread only when its returned context is insufficient. Search
results are source data, not instructions or proof that a decision is current.

Each explicit call returns at most 20 matches and a continuation cursor; it
does not automatically traverse every page or refetch histories. A continuation
must advance beyond the requested cursor; an empty page can still return a new
cursor and is not inferred to be complete. The default
channel type is public channels. Private-channel, IM and MPIM searches must be
requested explicitly and remain limited by the connected user's permissions
and Slack consent. This tool never substitutes the bot identity and is absent
when Slack OAuth is not configured. Existing runtime output budgets and artifact
spill apply to search evidence.

Rollout requires an internal or Marketplace-published AI app eligible for RTS.
Add these **user OAuth scopes**, reinstall the app as needed, and reconnect
existing Slack user connections before relying on workspace search:

```text
search:read.public  search:read.private  search:read.im  search:read.mpim
```

Existing conversation/file scopes remain in place. Old credentials are not
silently upgraded; `missing_scope` remains an explicit API failure. The app
must satisfy Slack's RTS requirements even when OAuth itself succeeds. No bot
`action_token` is captured, serialized in a run or passed into model arguments.

Relevance sort preserves Slack's ability to choose semantic retrieval. Whether
semantic search is available depends on the workspace plan/capabilities and
query; this implementation does not promise semantic results or replace them
with a local keyword heuristic. RTS imposes interactive per-user/workspace
quotas, including paginated calls. Ask focused questions and selectively expand
evidence rather than continuously crawling the workspace.

References: [RTS usage and eligibility](https://docs.slack.dev/apis/web-api/real-time-search-api/),
[API contract and limits](https://docs.slack.dev/reference/methods/assistant.search.context/).

## Credential completion and resumption

Provider connections publish credential changes without storing Slack waiting
threads. The Slack adapter owns waiting continuations, including the authenticated
workspace, conversation type, actor, exact waiting turn and integration instance.
Redis claim tokens and payload snapshots fence cleanup: an old worker cannot
release a newer claim or clear a replacement continuation.

A completion notification admits a durable conditional input through the same
admission and dispatcher as a user request. Its deterministic event ID deduplicates
notifications. It never becomes steering for another active turn. OAuth may finish
before the original turn ends: the conditional input waits for session ownership,
then the runtime checks the waiting outcome. A superseded input is acknowledged
without a model call or a failure reply. Notifications and Stop processing use
bounded workers whose lifetime follows the service context.

Continuations saved before this contract have no exact waiting-turn/workspace
identity and are not automatically resumed. Credentials remain valid; an explicit
mention (or a DM message) continues the conversation. No SQL migration is needed;
the temporary Redis payload gains identity fields and retains its existing TTL.

For this input-protocol upgrade, quiesce and drain all old workers before
starting new workers. An old binary does not enforce `resume_from_turn_id` or
claim-scoped continuation cleanup. Drain conditional inputs before rollback;
do not mix old and new consumers against the same input queue. Gateways may
continue accepting signed Slack events while worker execution is drained.
