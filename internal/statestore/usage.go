package statestore

import (
	"context"
	"fmt"
)

// UsageStore is the OPTIONAL narrow persistence seam for the live claude-subscription usage
// snapshots the davinci usage-probe reports (PUT /usage/{key}) and the editor reads (GET /usage).
// Kept SEPARATE from the frozen StateStore (ADR-0021 additive) — reached by a type-assertion, so
// no existing implementer/fake breaks and a store without it means the feature is unavailable.
//
// WHY PERSISTED (not the in-memory map it replaced): the gateway runs replicaCount>1. An
// in-memory map lives on ONE pod, so the probe's PUT lands on one replica while a round-robined
// GET hits another and returns {} — the editor's usage bar flickered on/off. A shared table makes
// every replica serve the same snapshot. The value is small + non-secret (utilization percentages
// + reset timestamps), stored verbatim.
type UsageStore interface {
	// PutUsage upserts a subscription's latest snapshot (by key, e.g. "admin"/"vendor"). The
	// snapshot is opaque, already-validated JSON.
	PutUsage(ctx context.Context, key string, snapshot []byte) error
	// ListUsage returns every stored snapshot keyed by subscription. An empty map (not an error)
	// when nothing has been reported yet.
	ListUsage(ctx context.Context) (map[string][]byte, error)
}

// Compile-time assertions that both stores satisfy the additive seam.
var (
	_ UsageStore = (*MemoryStore)(nil)
	_ UsageStore = (*PostgresStore)(nil)
)

// PutUsage upserts a subscription's snapshot in memory (last write wins per key).
func (s *MemoryStore) PutUsage(ctx context.Context, key string, snapshot []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("put usage: %w: empty key", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		s.usage = map[string][]byte{}
	}
	s.usage[key] = cloneBytes(snapshot)
	return nil
}

// ListUsage returns a copy of every stored snapshot.
func (s *MemoryStore) ListUsage(ctx context.Context) (map[string][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]byte, len(s.usage))
	for k, v := range s.usage {
		out[k] = cloneBytes(v)
	}
	return out, nil
}

// PutUsage upserts a subscription's snapshot in Postgres (INSERT ... ON CONFLICT on key).
func (s *PostgresStore) PutUsage(ctx context.Context, key string, snapshot []byte) error {
	if key == "" {
		return fmt.Errorf("put usage: %w: empty key", ErrInvalid)
	}
	const q = `
INSERT INTO usage_snapshots (key, snapshot, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (key) DO UPDATE SET
  snapshot = EXCLUDED.snapshot, updated_at = now()`
	if _, err := s.pool.Exec(ctx, q, key, snapshot); err != nil {
		return fmt.Errorf("put usage %q: %w", key, err)
	}
	return nil
}

// ListUsage returns every stored snapshot keyed by subscription.
func (s *PostgresStore) ListUsage(ctx context.Context) (map[string][]byte, error) {
	const q = `SELECT key, snapshot FROM usage_snapshots`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list usage: %w", err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var key string
		var snapshot []byte
		if err := rows.Scan(&key, &snapshot); err != nil {
			return nil, fmt.Errorf("list usage: %w", err)
		}
		out[key] = snapshot
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list usage: %w", err)
	}
	return out, nil
}
