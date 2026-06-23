package statestore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Credential is an ENCRYPTED-AT-REST credential the gateway holds on behalf of the director
// (Faz L3 — ADR-0049): the editor uploads the portable claude OAuth token once (authed), the
// gateway seals it (credstore AES-256-GCM) and persists ONLY the ciphertext + nonce here, and
// each agent fetches + decrypts it over the already-authed channel at startup — so no
// secret-file lives on any performer host and the editor is the single login point.
//
// The store NEVER sees plaintext: encryption/decryption happens in the gateway handler (with
// the master key from a k8s secret), so a database compromise alone does not reveal the token.
//
// ADDITIVE (ADR-0021): this type and the CredentialStore seam below are SEPARATE from the
// frozen StateStore interface — reached by an optional type-assertion, so no existing
// implementer/fake breaks and a store without it simply means the feature is unavailable.
type Credential struct {
	// Kind names the credential (e.g. "claude_oauth"). It is the primary key — one row per
	// kind, upserted on re-upload. Not secret.
	Kind string
	// Ciphertext is the sealed credential (AES-GCM ciphertext + tag). Opaque bytes; useless
	// without the master key.
	Ciphertext []byte
	// Nonce is the per-Seal random nonce needed to Open the ciphertext. Not secret, but must be
	// stored alongside the ciphertext.
	Nonce []byte
}

// CredentialStore is the OPTIONAL narrow persistence seam for L3 encrypted credentials
// (ADR-0049), kept SEPARATE from the frozen StateStore (ADR-0021 additive). Both PostgresStore
// and MemoryStore implement it; the gateway type-asserts it to serve the /agent/credentials
// endpoints (501 if absent). It persists ciphertext only — it has no knowledge of the master
// key or the plaintext token.
type CredentialStore interface {
	// PutCredential upserts the sealed credential (by Kind).
	PutCredential(ctx context.Context, c Credential) error
	// GetCredential returns the sealed credential for a kind, or ErrNotFound when none is stored.
	GetCredential(ctx context.Context, kind string) (Credential, error)
	// DeleteCredential removes a stored credential (logout propagation). Deleting an absent
	// kind is a no-op (no error) so logout is idempotent.
	DeleteCredential(ctx context.Context, kind string) error
}

// Compile-time assertions that both stores satisfy the additive seam.
var (
	_ CredentialStore = (*MemoryStore)(nil)
	_ CredentialStore = (*PostgresStore)(nil)
)

// cloneBytes returns a defensive copy so a stored credential can't be mutated by a caller
// holding the input/returned slice (the in-memory store keeps the only reference).
func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// PutCredential upserts the sealed credential in memory (last write wins per kind).
func (s *MemoryStore) PutCredential(ctx context.Context, c Credential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Kind == "" {
		return fmt.Errorf("put credential: %w: empty kind", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds[c.Kind] = Credential{Kind: c.Kind, Ciphertext: cloneBytes(c.Ciphertext), Nonce: cloneBytes(c.Nonce)}
	return nil
}

// GetCredential returns the sealed credential for a kind, or a wrapped ErrNotFound.
func (s *MemoryStore) GetCredential(ctx context.Context, kind string) (Credential, error) {
	if err := ctx.Err(); err != nil {
		return Credential{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.creds[kind]
	if !ok {
		return Credential{}, fmt.Errorf("get credential %q: %w", kind, ErrNotFound)
	}
	return Credential{Kind: c.Kind, Ciphertext: cloneBytes(c.Ciphertext), Nonce: cloneBytes(c.Nonce)}, nil
}

// DeleteCredential removes a stored credential (idempotent).
func (s *MemoryStore) DeleteCredential(ctx context.Context, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, kind)
	return nil
}

// PutCredential upserts the sealed credential in Postgres (INSERT ... ON CONFLICT on kind).
func (s *PostgresStore) PutCredential(ctx context.Context, c Credential) error {
	if c.Kind == "" {
		return fmt.Errorf("put credential: %w: empty kind", ErrInvalid)
	}
	const q = `
INSERT INTO agent_credentials (kind, ciphertext, nonce, updated_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (kind) DO UPDATE SET
  ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce, updated_at = now()`
	if _, err := s.pool.Exec(ctx, q, c.Kind, c.Ciphertext, c.Nonce); err != nil {
		return fmt.Errorf("put credential %q: %w", c.Kind, err)
	}
	return nil
}

// GetCredential returns the sealed credential for a kind, or a wrapped ErrNotFound.
func (s *PostgresStore) GetCredential(ctx context.Context, kind string) (Credential, error) {
	const q = `SELECT kind, ciphertext, nonce FROM agent_credentials WHERE kind = $1`
	var c Credential
	err := s.pool.QueryRow(ctx, q, kind).Scan(&c.Kind, &c.Ciphertext, &c.Nonce)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, fmt.Errorf("get credential %q: %w", kind, ErrNotFound)
	}
	if err != nil {
		return Credential{}, fmt.Errorf("get credential %q: %w", kind, err)
	}
	return c, nil
}

// DeleteCredential removes a stored credential (idempotent — deleting an absent kind is fine).
func (s *PostgresStore) DeleteCredential(ctx context.Context, kind string) error {
	const q = `DELETE FROM agent_credentials WHERE kind = $1`
	if _, err := s.pool.Exec(ctx, q, kind); err != nil {
		return fmt.Errorf("delete credential %q: %w", kind, err)
	}
	return nil
}
