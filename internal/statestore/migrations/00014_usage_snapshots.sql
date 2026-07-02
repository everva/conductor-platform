-- +goose Up
-- +goose StatementBegin
-- Usage snapshots (ADR-0021 additive): the LIVE claude-subscription utilization the davinci
-- usage-probe reports (PUT /usage/{key}) and the editor reads (GET /usage). Persisted (not
-- in-memory) so ALL gateway replicas serve the SAME data — with replicaCount>1 an in-memory map
-- lives on only one pod, so a round-robined GET returned {} half the time. One row per
-- subscription key (e.g. 'admin'/'vendor'), upserted each probe cycle. The body is small and
-- non-secret (utilization percentages + reset timestamps), stored verbatim as jsonb. The frozen
-- StateStore tables are unchanged.
CREATE TABLE IF NOT EXISTS usage_snapshots (
  key        text        PRIMARY KEY,
  snapshot   jsonb       NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS usage_snapshots;
-- +goose StatementEnd
