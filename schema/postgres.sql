-- Current PostgreSQL schema contract for Slack Copilot Agent.
--
-- Services only read and write these objects; they never execute DDL. Apply
-- this file with the database administration workflow of your choice before
-- starting a service. The statements are idempotent for clean installations.

CREATE TABLE IF NOT EXISTS agent_sessions (
    id TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS agent_runs (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    slack_channel TEXT NOT NULL DEFAULT '',
    slack_message_ts TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_session_started
    ON agent_runs(session_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_runs_slack_message
    ON agent_runs(slack_channel, slack_message_ts)
    WHERE slack_message_ts <> '';

CREATE TABLE IF NOT EXISTS agent_tool_spills (
    run_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    content TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (run_id, tool_name, tool_call_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_tool_spills_updated
    ON agent_tool_spills(updated_at DESC);

CREATE TABLE IF NOT EXISTS agent_run_steps (
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    seq BIGSERIAL,
    step_id TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    PRIMARY KEY (run_id, step_id)
);

CREATE INDEX IF NOT EXISTS idx_agent_run_steps_sequence
    ON agent_run_steps(run_id, seq);

CREATE TABLE IF NOT EXISTS agent_run_feedback (
    run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    value TEXT NOT NULL,
    user_id TEXT NOT NULL DEFAULT '',
    channel TEXT NOT NULL DEFAULT '',
    message_ts TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (run_id, source, user_id, created_at)
);

CREATE INDEX IF NOT EXISTS idx_agent_run_feedback_run
    ON agent_run_feedback(run_id, created_at);

CREATE TABLE IF NOT EXISTS reminders (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    channel TEXT NOT NULL,
    thread_ts TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    run_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at TIMESTAMPTZ,
    claim_until TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_reminders_due
    ON reminders(run_at) WHERE sent_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_reminders_user_pending
    ON reminders(user_id, run_at) WHERE sent_at IS NULL;

CREATE TABLE IF NOT EXISTS slack_event_inbox (
    event_id TEXT PRIMARY KEY,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued'
        CHECK (status IN ('queued', 'processing', 'completed', 'dead_letter')),
    received_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    claim_until TIMESTAMPTZ,
    claim_owner TEXT NOT NULL DEFAULT '',
    completed_at TIMESTAMPTZ,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_error TEXT NOT NULL DEFAULT '',
    dead_lettered_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_slack_event_inbox_expired
    ON slack_event_inbox(claim_until) WHERE status = 'processing';
CREATE INDEX IF NOT EXISTS idx_slack_event_inbox_pending
    ON slack_event_inbox(available_at, received_at) WHERE status = 'queued';

CREATE TABLE IF NOT EXISTS user_settings (
    user_id TEXT PRIMARY KEY,
    web_search_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS user_prompt_assets (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    source_file_id TEXT NOT NULL DEFAULT '',
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS user_prompt_assets_user_kind_idx
    ON user_prompt_assets(user_id, kind, active, name);
