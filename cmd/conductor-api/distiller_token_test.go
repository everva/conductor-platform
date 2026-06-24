package main

import (
	"context"
	"crypto/rand"
	"io"
	"testing"

	"github.com/everva/conductor-platform/internal/credstore"
	"github.com/everva/conductor-platform/internal/statestore"
)

// freshSealer builds a real AES-256 sealer with a random key.
func freshSealer(t *testing.T) *credstore.Sealer {
	t.Helper()
	key := make([]byte, credstore.KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("gen key: %v", err)
	}
	sealer, err := credstore.New(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	return sealer
}

// TestClaudeTokenProvider proves the Faz-R provider fetches + decrypts the SEALED claude
// credential — the exact path /distill uses in a pod with no ambient claude login.
func TestClaudeTokenProvider(t *testing.T) {
	sealer := freshSealer(t)
	var store statestore.StateStore = statestore.NewMemoryStore()
	cs := store.(statestore.CredentialStore)

	// Seal + store the subscription token under the shared claude credential kind.
	ct, nonce, err := sealer.Seal([]byte("sub-oauth-secret-123"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := cs.PutCredential(context.Background(), statestore.Credential{Kind: claudeCredentialKind, Ciphertext: ct, Nonce: nonce}); err != nil {
		t.Fatalf("put: %v", err)
	}

	provider := claudeTokenProvider(store, sealer)
	if provider == nil {
		t.Fatal("provider must be non-nil when store + sealer are configured")
	}
	tok, err := provider(context.Background())
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if tok != "sub-oauth-secret-123" {
		t.Fatalf("provider returned %q, want the decrypted token", tok)
	}
}

// TestClaudeTokenProvider_Disabled: no sealer (no master key) → nil provider, so the distiller
// falls back to the env's own subscription auth (local davinci), unchanged.
func TestClaudeTokenProvider_Disabled(t *testing.T) {
	store := statestore.NewMemoryStore()
	if claudeTokenProvider(store, nil) != nil {
		t.Fatal("a nil sealer must disable the provider (fall back to env auth)")
	}
}

// TestClaudeTokenProvider_MissingCredential: a configured provider with NO stored credential
// returns an error (a distill failure), never a silent unauthenticated call.
func TestClaudeTokenProvider_MissingCredential(t *testing.T) {
	provider := claudeTokenProvider(statestore.NewMemoryStore(), freshSealer(t))
	if provider == nil {
		t.Fatal("provider must be non-nil")
	}
	if _, err := provider(context.Background()); err == nil {
		t.Fatal("a missing credential must surface as an error")
	}
}
