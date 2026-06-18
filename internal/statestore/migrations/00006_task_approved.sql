-- +goose Up
-- +goose StatementBegin
-- Faz-1.5-b (governance N-10 human-hold / ADR-0021 additive growth): the operator
-- APPROVAL signal for a task HELD awaiting a human after a green gate becomes a
-- durable per-task flag so a separate conductorctl process can approve the
-- already-verified work and a later daemon tick merges the PRESERVED verified
-- branch without re-developing it. CreateTask defaults it to false.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS approved boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tasks DROP COLUMN IF EXISTS approved;
-- +goose StatementEnd
