-- +goose Up
-- +goose StatementBegin
-- Faz-1.5-a (ADR-0018 holdout isolation / ADR-0021 additive growth): the
-- Postgres-backed holdout store. A holdout is a set of injectable files keyed by
-- a holdout id; each row is one file (id, path) -> content. The bytea content is
-- the file body the verify gate injects TEMPORARILY into its throwaway
-- verify-worktree. This table is repo-EXTERNAL by construction (it lives in the
-- central Postgres, never in any product checkout) and the verify gate NEVER
-- copies it into the develop worktree or logs its contents (ADR-0018).
--
-- A pg:// locator "pg://holdouts/<id>" selects every row with that id; the file
-- set is { path -> content }. Paths are sanitized at READ time (".." / absolute
-- rejected) so a malicious row cannot inject outside the verify-worktree, the
-- same guard the filesystem store applies.
CREATE TABLE IF NOT EXISTS holdouts (
    id      text  NOT NULL,
    path    text  NOT NULL,
    content bytea NOT NULL,
    PRIMARY KEY (id, path)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS holdouts;
-- +goose StatementEnd
