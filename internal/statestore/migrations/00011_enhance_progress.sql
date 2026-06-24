-- +goose Up
-- +goose StatementBegin
-- Live ENHANCE progress (additive, ADR-0021): the agent streams claude's REAL activity
-- (📖 Okunuyor / 🔎 Aranıyor / 🤔 Düşünülüyor) into `progress`, stamping `progress_at` on each
-- update; the editor polls GetEnhanceJob and shows the live line + derives "idle for N s" from
-- now - progress_at. `progress` is NOT NULL DEFAULT '' so the add is a metadata-only change (no
-- table rewrite) and existing rows / older agents are unaffected; `progress_at` stays nullable
-- (zero until the first ping). Frozen StateStore tables are untouched.
ALTER TABLE enhance_jobs
  ADD COLUMN IF NOT EXISTS progress    text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS progress_at timestamptz;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE enhance_jobs
  DROP COLUMN IF EXISTS progress,
  DROP COLUMN IF EXISTS progress_at;
-- +goose StatementEnd
