-- +goose Up
-- +goose StatementBegin
-- F-2 (ADR-0020 abort follow-up / ADR-0021 additive growth): the control
-- reverse-channel ABORT signal becomes a durable per-task flag so a separate
-- conductorctl process can ask the running daemon to cancel the in-flight develop
-- and revert the task to a safe state. CreateTask defaults it to false.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS abort_requested boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE tasks DROP COLUMN IF EXISTS abort_requested;
-- +goose StatementEnd
