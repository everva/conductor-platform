// Package credstore provides authenticated symmetric encryption for credentials held at
// rest by the gateway (Faz L3 — ADR-0049): the portable claude OAuth token the editor
// uploads is encrypted before it touches Postgres, so a database compromise alone (without
// the master key, which lives only in a k8s secret) does not reveal the account-level token.
//
// DESIGN. AES-256-GCM (an AEAD): each Seal generates a fresh random 12-byte nonce and
// produces ciphertext+tag; Open verifies the tag, so any tampering (or a wrong key) fails
// closed with an error rather than returning garbage. This is the pragmatic "sealed-at-rest"
// scope: the data is the token, the master key is a 32-byte secret mounted from k8s. No
// external KMS dependency — the key never leaves the gateway process, and the gateway is the
// only component that decrypts (the agent fetches the plaintext over the already-authed
// channel — see the gateway handlers).
//
// TOKEN DISCIPLINE: nothing here logs plaintext or ciphertext; errors are shape-only ("open:
// authentication failed") and never include key/nonce/plaintext bytes.
package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// KeySize is the required master-key length: 32 bytes selects AES-256.
const KeySize = 32

// ErrNotConfigured is returned by helpers when no master key is available, so callers can
// fail CLOSED (refuse to store/serve a credential) rather than ever persisting plaintext.
var ErrNotConfigured = errors.New("credstore: no master key configured")

// Sealer encrypts and decrypts small credential blobs with a fixed master key. Safe for
// concurrent use (the underlying cipher.AEAD is stateless per call; each Seal makes its own
// nonce).
type Sealer struct {
	aead cipher.AEAD
}

// New builds a Sealer from a 32-byte AES-256 key. A key of the wrong length is rejected — the
// caller must supply exactly KeySize bytes (e.g. from KeyFromBase64).
func New(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("credstore: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credstore: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credstore: new gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// KeyFromBase64 decodes a standard-base64 master key and validates its length. The gateway
// reads the key from an env var sourced from a k8s secret; this turns it into raw bytes.
// Returns ErrNotConfigured for an empty string so the caller fails closed.
func KeyFromBase64(s string) ([]byte, error) {
	if s == "" {
		return nil, ErrNotConfigured
	}
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("credstore: decode key: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("credstore: decoded key must be %d bytes, got %d", KeySize, len(key))
	}
	return key, nil
}

// Seal encrypts plaintext, returning the ciphertext (including the GCM tag) and the fresh
// random nonce used. Store both; Open needs the nonce. The nonce is not secret (it is safe to
// store alongside the ciphertext) but MUST be unique per Seal under the same key — guaranteed
// here by crypto/rand.
func (s *Sealer) Seal(plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("credstore: read nonce: %w", err)
	}
	ciphertext = s.aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// Open decrypts ciphertext with its nonce, verifying the GCM authentication tag. A wrong key,
// a wrong nonce, or any tampering fails with an error (never silent corruption). The error is
// shape-only — it carries no key/nonce/plaintext material.
func (s *Sealer) Open(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != s.aead.NonceSize() {
		return nil, fmt.Errorf("credstore: nonce must be %d bytes, got %d", s.aead.NonceSize(), len(nonce))
	}
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// Deliberately opaque: do not leak whether it was a bad key, tag, or nonce.
		return nil, errors.New("credstore: open: authentication failed")
	}
	return plaintext, nil
}
