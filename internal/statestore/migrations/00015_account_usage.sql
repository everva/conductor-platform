-- +goose Up
-- +goose StatementBegin
-- Account-centric Claude usage snapshots (ADR-0021 additive), SEPARATE from usage_snapshots.
-- usage_snapshots is keyed by Conductor ROLE (admin/vendor) for the editor's fleet-oriented bar;
-- this table is keyed by ACCOUNT (a per-email slug) for the standalone "Pulse" usage HUD, which
-- groups by mail account and can include monitor-only accounts Conductor never uses. The davinci
-- account-usage-probe queries each account's isolated login and upserts one row per slug. Body is
-- small + non-secret (name/email + utilization %s + reset times), stored verbatim as jsonb.
CREATE TABLE IF NOT EXISTS account_usage_snapshots (
  slug       text        PRIMARY KEY,
  snapshot   jsonb       NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS account_usage_snapshots;
-- +goose StatementEnd
