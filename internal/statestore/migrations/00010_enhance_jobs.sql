-- +goose Up
-- +goose StatementBegin
-- Intake ENHANCE jobs (agent-side, code-aware): the director writes a rough Turkish
-- request in intake and clicks "Enhance"; the gateway records a pending job here, an
-- agent (which has the project's repo cloned + claude) claims it, runs claude read-only
-- over the real code, and writes back a detailed Turkish spec. The editor polls for the
-- result and fills the composer. ADDITIVE (ADR-0021): a separate table + EnhanceStore
-- seam, the frozen StateStore tables are untouched.
CREATE TABLE IF NOT EXISTS enhance_jobs (
  id         text        PRIMARY KEY,
  project_id text        NOT NULL,
  rough_spec text        NOT NULL DEFAULT '',
  status     text        NOT NULL DEFAULT 'pending',  -- pending | running | done | failed
  result     text        NOT NULL DEFAULT '',
  error      text        NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- Claim path queries the oldest pending job per project.
CREATE INDEX IF NOT EXISTS idx_enhance_jobs_project_status_created
  ON enhance_jobs (project_id, status, created_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS enhance_jobs;
-- +goose StatementEnd
