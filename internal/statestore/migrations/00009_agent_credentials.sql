-- +goose Up
-- +goose StatementBegin
-- Faz L3 (ADR-0049, ADR-0021 additive): the gateway-distributed encrypted credential store.
-- The editor uploads the director's portable claude OAuth token once (authed); the gateway
-- SEALS it (credstore AES-256-GCM, master key from a k8s secret) and persists ONLY the
-- ciphertext + per-Seal nonce here. Each performer agent fetches + decrypts it over the
-- already-authed channel at startup, so no secret-file lives on any host and the editor is the
-- single login point. The plaintext token NEVER touches this table; a DB compromise alone
-- (without the master key) does not reveal it. One row per kind (e.g. 'claude_oauth'), upserted
-- on re-upload. The frozen StateStore tables are unchanged.
CREATE TABLE IF NOT EXISTS agent_credentials (
  kind       text        PRIMARY KEY,
  ciphertext bytea       NOT NULL,
  nonce      bytea       NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS agent_credentials;
-- +goose StatementEnd
