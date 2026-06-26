-- +goose Up
-- +goose StatementBegin
-- Surface WHY a task stalled (additive, ADR-0021): when the agent reports a non-pass outcome the
-- gateway now persists the report Summary onto the task as `last_error`, so the director's board
-- shows the reason ("agent run failed: …malformed verdict…", "gate unresolved after N rounds —
-- needs user", "holdout: no holdout command configured") instead of a bare "blocked". The reason
-- already reached the event stream (KindDecision summary); this puts it on the task the board
-- renders. NOT NULL DEFAULT '' = metadata-only add (no table rewrite); existing rows and older
-- agents are unaffected. Cleared when the task starts a fresh attempt (claim→running) or is
-- retried. Frozen StateStore tables are otherwise untouched.
ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tasks
  DROP COLUMN IF EXISTS last_error;
-- +goose StatementEnd
