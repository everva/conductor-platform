-- +goose Up
-- +goose StatementBegin
-- Live event stream for observability (ADR-0011). This is the queryable history
-- behind the realtime LISTEN/NOTIFY push; distinct from the audit/journal
-- (ADR-0010) — that is durable audit, this is live monitoring. The event bus
-- both INSERTs here and pg_notify()s so subscribers on other hosts get pushed
-- the row in realtime.
CREATE TABLE IF NOT EXISTS events (
    id         text PRIMARY KEY,
    ts         timestamptz NOT NULL DEFAULT now(),
    project    text NOT NULL DEFAULT '',
    task       text NOT NULL DEFAULT '',
    phase      text NOT NULL DEFAULT '',
    kind       text NOT NULL DEFAULT '',
    payload    jsonb NOT NULL DEFAULT '{}'::jsonb
);

-- Index for streaming/replaying a project's (and a task's) history in order.
CREATE INDEX IF NOT EXISTS idx_events_project_ts ON events (project, ts);
CREATE INDEX IF NOT EXISTS idx_events_task_ts ON events (task, ts);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS events;
-- +goose StatementEnd
