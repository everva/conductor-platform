package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// fakeHoldoutStore is a hermetic holdoutStore: it records the last Store call and replays a canned
// Fetch, so the endpoint logic (base64 decode/encode, status mapping) is tested without a pgxpool.
type fakeHoldoutStore struct {
	storedID    string
	storedFiles map[string][]byte
	storeErr    error
	fetch       verify.Holdout
	fetchErr    error
}

func (f *fakeHoldoutStore) Store(_ context.Context, id string, files map[string][]byte) (string, error) {
	if f.storeErr != nil {
		return "", f.storeErr
	}
	f.storedID = id
	f.storedFiles = files
	return "pg://holdouts/" + id, nil
}

func (f *fakeHoldoutStore) Fetch(_ context.Context, _ string) (verify.Holdout, error) {
	if f.fetchErr != nil {
		return verify.Holdout{}, f.fetchErr
	}
	return f.fetch, nil
}

func holdoutServer(hs holdoutStore) *apiServer {
	return &apiServer{store: statestore.NewMemoryStore(), token: testToken, clock: fixedClock, holdouts: hs, logger: testLogger()}
}

func TestPutHoldout_StoresDecodedFiles(t *testing.T) {
	fake := &fakeHoldoutStore{}
	s := holdoutServer(fake)

	body := `{"files":{"healthz_test.ts":"` + base64.StdEncoding.EncodeToString([]byte("playwright test\n")) + `"}}`
	rec := doBody(t, s, http.MethodPut, "/holdouts/S-1", bearer(), body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp holdoutLocatorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Locator != "pg://holdouts/S-1" {
		t.Fatalf("locator = %q", resp.Locator)
	}
	// The store received the id + the DECODED (raw) file bytes, not the base64.
	if fake.storedID != "S-1" {
		t.Fatalf("stored id = %q", fake.storedID)
	}
	if got := string(fake.storedFiles["healthz_test.ts"]); got != "playwright test\n" {
		t.Fatalf("stored content = %q (must be base64-decoded)", got)
	}
}

func TestPutHoldout_Errors(t *testing.T) {
	// 501 when no store configured.
	rec := doBody(t, holdoutServer(nil), http.MethodPut, "/holdouts/S-1", bearer(), `{"files":{"a":"YQ=="}}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no-store status = %d, want 501", rec.Code)
	}
	// 400 empty files.
	rec = doBody(t, holdoutServer(&fakeHoldoutStore{}), http.MethodPut, "/holdouts/S-1", bearer(), `{"files":{}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty-files status = %d, want 400", rec.Code)
	}
	// 400 non-base64 content.
	rec = doBody(t, holdoutServer(&fakeHoldoutStore{}), http.MethodPut, "/holdouts/S-1", bearer(), `{"files":{"a":"!!notb64!!"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad-base64 status = %d, want 400", rec.Code)
	}
	// 400 a Store validation error (unsafe path) is surfaced to the client.
	bad := &fakeHoldoutStore{storeErr: errors.New(`holdout: store "S-1" has unsafe path "../x"`)}
	rec = doBody(t, holdoutServer(bad), http.MethodPut, "/holdouts/S-1", bearer(), `{"files":{"a":"YQ=="}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("validation status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// 401 without auth.
	rec = doBody(t, holdoutServer(&fakeHoldoutStore{}), http.MethodPut, "/holdouts/S-1", "", `{"files":{"a":"YQ=="}}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", rec.Code)
	}
}

func TestGetHoldout_ServesBase64(t *testing.T) {
	fake := &fakeHoldoutStore{fetch: verify.Holdout{Name: "S-1", Files: map[string][]byte{"t.ts": []byte("body\n")}}}
	rec := doBody(t, holdoutServer(fake), http.MethodGet, "/agent/holdout?ref=pg://holdouts/S-1", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp holdoutFilesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "S-1" {
		t.Fatalf("name = %q", resp.Name)
	}
	if got, _ := base64.StdEncoding.DecodeString(resp.Files["t.ts"]); string(got) != "body\n" {
		t.Fatalf("file content round-trip failed: %q", resp.Files["t.ts"])
	}
}

func TestGetHoldout_Errors(t *testing.T) {
	// 400 missing ref.
	rec := doBody(t, holdoutServer(&fakeHoldoutStore{}), http.MethodGet, "/agent/holdout", bearer(), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-ref status = %d, want 400", rec.Code)
	}
	// 501 no store.
	rec = doBody(t, holdoutServer(nil), http.MethodGet, "/agent/holdout?ref=pg://holdouts/X", bearer(), "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no-store status = %d, want 501", rec.Code)
	}
	// 404 ErrNotFound AND any fetch error (no info leak between absent vs store error).
	rec = doBody(t, holdoutServer(&fakeHoldoutStore{fetchErr: statestore.ErrNotFound}), http.MethodGet, "/agent/holdout?ref=pg://holdouts/X", bearer(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("notfound status = %d, want 404", rec.Code)
	}
	rec = doBody(t, holdoutServer(&fakeHoldoutStore{fetchErr: errors.New("no rows for id")}), http.MethodGet, "/agent/holdout?ref=pg://holdouts/X", bearer(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fetch-error status = %d, want 404", rec.Code)
	}
}
