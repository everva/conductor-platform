-- +goose Up
-- +goose StatementBegin
-- Host registry for the agent-per-host model (ADR-0024, 2B-1): the conductor
-- daemon runs on every host and self-registers its identity + capabilities here
-- on startup, then heartbeats periodically. Capability-routing (2B-2) reads this
-- to route a lane to a host only when lane.requires ⊆ host.capabilities
-- (ADR-0008). Capabilities are stored as jsonb (mirroring tasks.requires) so the
-- Postgres and in-memory stores round-trip the slice field identically.
CREATE TABLE IF NOT EXISTS hosts (
    id             text PRIMARY KEY,
    capabilities   jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_heartbeat timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS hosts;
-- +goose StatementEnd
