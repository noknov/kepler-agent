-- Apply before deploying versioned credential refresh writers.
ALTER TABLE user_connections
    ADD COLUMN IF NOT EXISTS revision TEXT NOT NULL DEFAULT gen_random_uuid()::text;
