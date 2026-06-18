-- +goose Up
-- +goose StatementBegin
-- ADR-0021: pause becomes a first-class, observable Project run-state, replacing
-- the ADR-0020 marker-task workaround. CreateProject defaults to running (false).
ALTER TABLE projects ADD COLUMN IF NOT EXISTS paused boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE projects DROP COLUMN IF EXISTS paused;
-- +goose StatementEnd
