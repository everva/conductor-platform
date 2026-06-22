-- +goose Up
-- +goose StatementBegin
-- Faz-P P2b (ADR-0041, ADR-0021 additive growth): the FULL-context diff of a task's
-- verified branch vs the project base, persisted OUT-OF-BAND from the bounded KindDiff
-- event. The event (events.DiffSummary) is capped at ~8KB for the Postgres LISTEN/NOTIFY
-- bus, too small for whole-file content; this table holds the richer full-context patch so
-- the editor can fetch it (GET /projects/{id}/tasks/{task}/diff) and render a full-file
-- NATIVE vscode.diff. One row per (project, task), upserted at the green gate; the bounded
-- NOTIFY event and the frozen StateStore tables are unchanged.
CREATE TABLE IF NOT EXISTS task_diffs (
  project_id text        NOT NULL,
  task_id    text        NOT NULL,
  base       text        NOT NULL DEFAULT '',
  branch     text        NOT NULL DEFAULT '',
  patch      text        NOT NULL DEFAULT '',
  truncated  boolean     NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, task_id)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS task_diffs;
-- +goose StatementEnd
