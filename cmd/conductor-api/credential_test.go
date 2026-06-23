package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/everva/conductor-platform/internal/credstore"
	"github.com/everva/conductor-platform/internal/statestore"
)

// credServer builds an apiServer with a real AES-256-GCM sealer (random key), an in-memory
// store (which implements CredentialStore), and the fixed test token.
func credServer(t *testing.T) (*apiServer, statestore.StateStore) {
	t.Helper()
	key := make([]byte, credstore.KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("gen key: %v", err)
	}
	sealer, err := credstore.New(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	store := statestore.NewMemoryStore()
	return &apiServer{store: store, token: testToken, clock: fixedClock, sealer: sealer}, store
}

const claudeKind = "claude_oauth"
const fakeToken = "sk-ant-oat-FAKE-PORTABLE-TOKEN-VALUE"

func TestCredential_PutGetRoundTrip(t *testing.T) {
	s, store := credServer(t)

	rec := doBody(t, s, http.MethodPut, "/agent/credentials/"+claudeKind, bearer(), `{"token":"`+fakeToken+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// PUT returns no body (the token is never echoed back).
	if rec.Body.Len() != 0 {
		t.Fatalf("PUT body must be empty, got %q", rec.Body.String())
	}

	// At rest the stored row is CIPHERTEXT — it must not contain the plaintext token.
	cs := store.(statestore.CredentialStore)
	stored, err := cs.GetCredential(context.Background(), claudeKind)
	if err != nil {
		t.Fatalf("stored credential: %v", err)
	}
	if bytes.Contains(stored.Ciphertext, []byte(fakeToken)) {
		t.Fatalf("ciphertext at rest leaks the plaintext token")
	}

	// GET decrypts and returns the original token.
	rec = do(t, s, http.MethodGet, "/agent/credentials/"+claudeKind, bearer())
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Token != fakeToken {
		t.Fatalf("round-trip token = %q, want %q", out.Token, fakeToken)
	}
}

func TestCredential_Upsert(t *testing.T) {
	s, _ := credServer(t)
	doBody(t, s, http.MethodPut, "/agent/credentials/"+claudeKind, bearer(), `{"token":"first"}`)
	doBody(t, s, http.MethodPut, "/agent/credentials/"+claudeKind, bearer(), `{"token":"second"}`)
	rec := do(t, s, http.MethodGet, "/agent/credentials/"+claudeKind, bearer())
	var out struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Token != "second" {
		t.Fatalf("upsert did not overwrite: got %q", out.Token)
	}
}

func TestCredential_GetMissing404(t *testing.T) {
	s, _ := credServer(t)
	rec := do(t, s, http.MethodGet, "/agent/credentials/"+claudeKind, bearer())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing status = %d, want 404", rec.Code)
	}
}

func TestCredential_Delete(t *testing.T) {
	s, _ := credServer(t)
	doBody(t, s, http.MethodPut, "/agent/credentials/"+claudeKind, bearer(), `{"token":"`+fakeToken+`"}`)

	rec := do(t, s, http.MethodDelete, "/agent/credentials/"+claudeKind, bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", rec.Code)
	}
	// Gone now.
	rec = do(t, s, http.MethodGet, "/agent/credentials/"+claudeKind, bearer())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET after delete status = %d, want 404", rec.Code)
	}
	// Idempotent: a second delete still succeeds.
	rec = do(t, s, http.MethodDelete, "/agent/credentials/"+claudeKind, bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("idempotent DELETE status = %d, want 204", rec.Code)
	}
}

func TestCredential_FailClosedWithoutKey(t *testing.T) {
	// No sealer configured (the master key is unset) → PUT/GET must fail CLOSED (503), never
	// store or serve plaintext. DELETE still works (it does not decrypt).
	store := statestore.NewMemoryStore()
	s := &apiServer{store: store, token: testToken, clock: fixedClock} // sealer == nil

	rec := doBody(t, s, http.MethodPut, "/agent/credentials/"+claudeKind, bearer(), `{"token":"`+fakeToken+`"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT without key status = %d, want 503", rec.Code)
	}
	// Nothing was stored (*MemoryStore implements CredentialStore directly).
	if _, err := store.GetCredential(context.Background(), claudeKind); err == nil {
		t.Fatalf("a credential was stored despite no master key (must fail closed)")
	}
	rec = do(t, s, http.MethodGet, "/agent/credentials/"+claudeKind, bearer())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET without key status = %d, want 503", rec.Code)
	}
}

func TestCredential_PutEmptyToken400(t *testing.T) {
	s, _ := credServer(t)
	rec := doBody(t, s, http.MethodPut, "/agent/credentials/"+claudeKind, bearer(), `{"token":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT empty token status = %d, want 400", rec.Code)
	}
}

func TestCredential_Unauthorized(t *testing.T) {
	s, _ := credServer(t)
	for _, m := range []string{http.MethodPut, http.MethodGet, http.MethodDelete} {
		rec := do(t, s, m, "/agent/credentials/"+claudeKind, "") // no bearer
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without auth status = %d, want 401", m, rec.Code)
		}
	}
}

// TestCredential_TokenNeverLogged proves the token does not reach the server log on either the
// happy path OR the decrypt-error path (the one place a credstore error is logged). The token
// crosses only the request/response body.
func TestCredential_TokenNeverLogged(t *testing.T) {
	var logBuf bytes.Buffer
	s, _ := credServer(t)
	s.logger = slog.New(slog.NewTextHandler(&logBuf, nil))

	// Happy path PUT + GET.
	doBody(t, s, http.MethodPut, "/agent/credentials/"+claudeKind, bearer(), `{"token":"`+fakeToken+`"}`)
	do(t, s, http.MethodGet, "/agent/credentials/"+claudeKind, bearer())

	// Force the decrypt-error log path: swap in a DIFFERENT key so Open fails → serverError logs
	// "credential: open". The log must still carry no token/ciphertext.
	key2 := make([]byte, credstore.KeySize)
	if _, err := io.ReadFull(rand.Reader, key2); err != nil {
		t.Fatalf("gen key2: %v", err)
	}
	other, _ := credstore.New(key2)
	s.sealer = other
	rec := do(t, s, http.MethodGet, "/agent/credentials/"+claudeKind, bearer())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET after key rotation status = %d, want 500", rec.Code)
	}

	if bytes.Contains(logBuf.Bytes(), []byte(fakeToken)) {
		t.Fatalf("the token leaked into the server log")
	}
}
