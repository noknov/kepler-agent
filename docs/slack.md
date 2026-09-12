# Hosted Slack

Slack receives and presents hosted turns. Tools execute in the worker's
operator-managed workspace, using hosted policy and configured credentials.
Use the deployment repository to start gateway, worker, PostgreSQL, and Redis.

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
```

Use Slack's Agent experience (`agent_view`) for the corresponding presentation.
Enable only the features appropriate to your Slack app installation; event
access also depends on its permissions and conversation membership.

## Start a conversation

Mention the agent or use its supported direct-message entry. Replies continue
in the Slack thread under its ownership policy. User integration connections
are separate from Slack sign-in and may be requested before a tool can run.
See [tools and connections](tools.md).

## Review pull requests

Ask for a review and include one to four full GitHub PR URLs. No slash command
is required. A secondary-model router classifies new conversations; the code
review workflow validates URLs from the original message. Routing failures
fall back to general conversation.

The workflow instructs the lead to inspect an immutable PR head, assign bounded
read-only reviewer tasks by risk, verify candidate findings, and return a
consolidated answer. Workers have separate transcripts; their output is
internal to the lead. These are workflow instructions and execution structure,
not a guarantee that every model finding is correct.

Reply in the same thread to follow up on the original PR scope. A new root
message starts another conversation. CLI and Web do not install this Slack
router. See [runtime and delegation](runtime.md) for scope propagation.

## Delivery and recovery

PostgreSQL inbox claims, leases, retries, and deterministic message identities
support recovery. Delivery remains at least once across external systems;
Slack and PostgreSQL do not share a transaction. Do not infer that a request
failed solely because the visible answer is missing. Inspect the run and tool
result before repeating an external action.

For readiness, dead letters, shutdown, and run inspection, use
[operations](operations.md). For model and locale settings, use
[configuration](configuration.md).
