-- +goose Up
-- +goose StatementBegin
-- Schema for the conductor platform's authoritative intent (ADR-0010, ADR-0013).
-- Only intent/config/pointer/lease live here; observed runtime truth (e.g. whether
-- a branch merged) is derived from git/gh each tick and never cached (ADR-0010 §3).

CREATE TABLE IF NOT EXISTS projects (
    id                text PRIMARY KEY,
    repo              text NOT NULL DEFAULT '',
    base_branch       text NOT NULL DEFAULT '',
    host_id           text NOT NULL DEFAULT '',
    readiness         text NOT NULL DEFAULT '',
    recipe_pointer    text NOT NULL DEFAULT '',
    governance_policy text NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS tasks (
    id           text PRIMARY KEY,
    project_id   text NOT NULL DEFAULT '',
    lane         text NOT NULL DEFAULT '',
    tier         text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT '',
    requires     jsonb NOT NULL DEFAULT '[]'::jsonb,
    deps         jsonb NOT NULL DEFAULT '[]'::jsonb,
    branch       text NOT NULL DEFAULT '',
    scenario_id  text NOT NULL DEFAULT '',
    retry_count  integer NOT NULL DEFAULT 0
);

-- Index for ListTasks(project_id).
CREATE INDEX IF NOT EXISTS idx_tasks_project_id ON tasks (project_id);

-- One lease row per project: project_id is the primary key, which structurally
-- enforces "one active lease per repo" host-spanning (ADR-0008, ADR-0010). The
-- atomic INSERT plus this constraint is what makes concurrent AcquireLease
-- produce exactly one winner across hosts.
CREATE TABLE IF NOT EXISTS leases (
    project_id   text PRIMARY KEY,
    host_id      text NOT NULL DEFAULT '',
    task_id      text NOT NULL DEFAULT '',
    acquired_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS scenarios (
    id           text PRIMARY KEY,
    project_id   text NOT NULL DEFAULT '',
    title        text NOT NULL DEFAULT '',
    lane         text NOT NULL DEFAULT '',
    tier         text NOT NULL DEFAULT '',
    deps         jsonb NOT NULL DEFAULT '[]'::jsonb,
    acceptance   jsonb NOT NULL DEFAULT '[]'::jsonb,
    holdout_ref  text NOT NULL DEFAULT ''
);

-- Index for ListScenarios(project_id).
CREATE INDEX IF NOT EXISTS idx_scenarios_project_id ON scenarios (project_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS scenarios;
DROP TABLE IF EXISTS leases;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS projects;
-- +goose StatementEnd
