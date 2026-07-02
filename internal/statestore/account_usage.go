package statestore

import (
	"context"
	"fmt"
)

// AccountUsageStore is the OPTIONAL persistence seam for the ACCOUNT-centric Claude usage snapshots
// the davinci account-usage-probe reports (PUT /accounts/usage/{slug}) and the Pulse HUD reads
// (GET /accounts/usage). Kept SEPARATE from both the frozen StateStore (ADR-0021 additive) and the
// role-keyed UsageStore: this one is keyed by a per-EMAIL account slug and may include monitor-only
// accounts Conductor never runs. Same persistence rationale as UsageStore — the gateway runs
// replicaCount>1, so an in-memory map would flicker; a shared table makes every replica agree.
type AccountUsageStore interface {
	// PutAccountUsage upserts an account's latest snapshot (by slug). The snapshot is opaque,
	// already-validated JSON (name/email + utilization windows).
	PutAccountUsage(ctx context.Context, slug string, snapshot []byte) error
	// ListAccountUsage returns every stored snapshot keyed by slug. Empty map when none reported.
	ListAccountUsage(ctx context.Context) (map[string][]byte, error)
}

// Compile-time assertions that both stores satisfy the additive seam.
var (
	_ AccountUsageStore = (*MemoryStore)(nil)
	_ AccountUsageStore = (*PostgresStore)(nil)
)

// PutAccountUsage upserts an account's snapshot in memory (last write wins per slug).
func (s *MemoryStore) PutAccountUsage(ctx context.Context, slug string, snapshot []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if slug == "" {
		return fmt.Errorf("put account usage: %w: empty slug", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accountUsage == nil {
		s.accountUsage = map[string][]byte{}
	}
	s.accountUsage[slug] = cloneBytes(snapshot)
	return nil
}

// ListAccountUsage returns a copy of every stored account snapshot.
func (s *MemoryStore) ListAccountUsage(ctx context.Context) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]byte, len(s.accountUsage))
	for k, v := range s.accountUsage {
		out[k] = cloneBytes(v)
	}
	return out, nil
}

// PutAccountUsage upserts an account's snapshot in Postgres (INSERT ... ON CONFLICT on slug).
func (s *PostgresStore) PutAccountUsage(ctx context.Context, slug string, snapshot []byte) error {
	if slug == "" {
		return fmt.Errorf("put account usage: %w: empty slug", ErrInvalid)
	}
	const q = `
INSERT INTO account_usage_snapshots (slug, snapshot, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (slug) DO UPDATE SET
  snapshot = EXCLUDED.snapshot, updated_at = now()`
	if _, err := s.pool.Exec(ctx, q, slug, snapshot); err != nil {
		return fmt.Errorf("put account usage %q: %w", slug, err)
	}
	return nil
}

// ListAccountUsage returns every stored account snapshot keyed by slug.
func (s *PostgresStore) ListAccountUsage(ctx context.Context) (map[string][]byte, error) {
	const q = `SELECT slug, snapshot FROM account_usage_snapshots`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list account usage: %w", err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var slug string
		var snapshot []byte
		if err := rows.Scan(&slug, &snapshot); err != nil {
			return nil, fmt.Errorf("list account usage: %w", err)
		}
		out[slug] = snapshot
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list account usage: %w", err)
	}
	return out, nil
}
