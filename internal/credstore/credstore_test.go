package credstore

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"testing"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return key
}

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := New(newKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	plain := []byte("sk-ant-oat-portable-token-value")
	ct, nonce, err := s.Seal(plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	// The ciphertext must NOT contain the plaintext (it is genuinely encrypted).
	if bytes.Contains(ct, plain) {
		t.Fatalf("ciphertext leaks plaintext")
	}
	got, err := s.Open(ct, nonce)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestSealUsesFreshNoncePerCall(t *testing.T) {
	s, _ := New(newKey(t))
	_, n1, _ := s.Seal([]byte("x"))
	_, n2, _ := s.Seal([]byte("x"))
	if bytes.Equal(n1, n2) {
		t.Fatalf("nonce must be unique per Seal")
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	s1, _ := New(newKey(t))
	s2, _ := New(newKey(t))
	ct, nonce, _ := s1.Seal([]byte("secret"))
	if _, err := s2.Open(ct, nonce); err == nil {
		t.Fatalf("Open with the wrong key must fail")
	}
}

func TestOpenTamperedCiphertextFails(t *testing.T) {
	s, _ := New(newKey(t))
	ct, nonce, _ := s.Seal([]byte("secret"))
	ct[0] ^= 0xff // flip a bit — the GCM tag must reject it.
	if _, err := s.Open(ct, nonce); err == nil {
		t.Fatalf("Open of tampered ciphertext must fail")
	}
}

func TestNewRejectsWrongKeyLength(t *testing.T) {
	if _, err := New([]byte("too-short")); err == nil {
		t.Fatalf("New must reject a non-32-byte key")
	}
}

func TestKeyFromBase64(t *testing.T) {
	raw := newKey(t)
	enc := base64.StdEncoding.EncodeToString(raw)
	got, err := KeyFromBase64(enc)
	if err != nil {
		t.Fatalf("KeyFromBase64: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("decoded key mismatch")
	}
	// Empty → ErrNotConfigured (caller fails closed).
	if _, err := KeyFromBase64(""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("empty key must return ErrNotConfigured, got %v", err)
	}
	// Wrong decoded length → error.
	if _, err := KeyFromBase64(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatalf("a base64 of a non-32-byte key must error")
	}
}

func TestOpenRejectsWrongNonceSize(t *testing.T) {
	s, _ := New(newKey(t))
	ct, _, _ := s.Seal([]byte("secret"))
	if _, err := s.Open(ct, []byte("short")); err == nil {
		t.Fatalf("Open must reject a wrong-size nonce")
	}
}
