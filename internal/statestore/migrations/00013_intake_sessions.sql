-- +goose Up
-- +goose StatementBegin
-- Intake CONVERSATION history (additive, ADR-0021): the intake "describe → distill → review"
-- chat was EPHEMERAL (client-only state, lost on reload/project-switch). The director asked to
-- see prior conversations per project (Claude-Code-style history). The web now UPSERTS each
-- conversation here — id (web-minted, stable per conversation), project_id, a title derived from
-- the first turn, the full message thread as JSONB, and the last authored YAML (optional, so a
-- reopened session can resume editing). The director opens the history list per project and
-- reloads a conversation. PRIVACY: this is OPT-IN persistence the director chose; the distiller
-- still logs nothing. ADDITIVE: a separate table + IntakeSessionStore seam — the frozen
-- StateStore tables are untouched.
CREATE TABLE IF NOT EXISTS intake_sessions (
  id         text        PRIMARY KEY,
  project_id text        NOT NULL,
  title      text        NOT NULL DEFAULT '',
  messages   jsonb       NOT NULL DEFAULT '[]',
  result     text        NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- The history list queries a project's sessions newest-first.
CREATE INDEX IF NOT EXISTS idx_intake_sessions_project_created
  ON intake_sessions (project_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS intake_sessions;
-- +goose StatementEnd
