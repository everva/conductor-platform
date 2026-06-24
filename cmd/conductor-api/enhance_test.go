package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestEnhance_Lifecycle drives the full agent-side enhance round-trip over the gateway:
// director creates a job → agent claims it → agent posts the spec → director polls the result.
func TestEnhance_Lifecycle(t *testing.T) {
	store := statestore.NewMemoryStore()
	seedProject(t, store, "p")
	s := &apiServer{store: store, token: testToken, clock: fixedClock}

	// 1) Director creates an enhance job.
	rec := doBody(t, s, http.MethodPost, "/projects/p/enhance", bearer(), `{"rough_spec":"servis sirketini kaldir"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created enhanceJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != statestore.EnhancePending {
		t.Fatalf("create returned %+v", created)
	}

	// 2) Agent claims the next pending job.
	rec = do(t, s, http.MethodGet, "/projects/p/agent/enhance/next", bearer())
	if rec.Code != http.StatusOK {
		t.Fatalf("claim: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var claim agentEnhanceClaim
	if err := json.Unmarshal(rec.Body.Bytes(), &claim); err != nil {
		t.Fatal(err)
	}
	if claim.ID != created.ID || claim.RoughSpec != "servis sirketini kaldir" {
		t.Fatalf("claim mismatch: %+v", claim)
	}

	// 3) Agent posts the produced Turkish spec.
	rec = doBody(t, s, http.MethodPost, "/projects/p/agent/enhance/"+claim.ID+"/result", bearer(), `{"result":"## Detayli Turkce spec","error":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("result: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// 4) Director polls and sees the done result.
	rec = do(t, s, http.MethodGet, "/projects/p/enhance/"+created.ID, bearer())
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got enhanceJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != statestore.EnhanceDone || got.Result != "## Detayli Turkce spec" {
		t.Fatalf("poll returned %+v", got)
	}

	// 5) No more pending → claim is 204.
	rec = do(t, s, http.MethodGet, "/projects/p/agent/enhance/next", bearer())
	if rec.Code != http.StatusNoContent {
		t.Fatalf("empty claim: status=%d, want 204", rec.Code)
	}
}

func TestEnhance_RequiresRoughSpec(t *testing.T) {
	store := statestore.NewMemoryStore()
	seedProject(t, store, "p")
	s := &apiServer{store: store, token: testToken, clock: fixedClock}
	rec := doBody(t, s, http.MethodPost, "/projects/p/enhance", bearer(), `{"rough_spec":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank rough_spec: status=%d, want 400", rec.Code)
	}
}

// A store with no EnhanceStore → 501 (graceful), not a panic.
func TestEnhance_NotImplementedWhenStoreLacksPersistence(t *testing.T) {
	s := &apiServer{store: failingStore{}, token: testToken, clock: fixedClock}
	rec := doBody(t, s, http.MethodPost, "/projects/p/enhance", bearer(), `{"rough_spec":"x"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d, want 501 (store has no EnhanceStore); body=%s", rec.Code, rec.Body.String())
	}
}
