package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// PUT /usage/{key} stores the snapshot; GET /usage returns it. Bad (non-JSON) body is rejected.
// The store (MemoryStore implements UsageStore) is shared, so — unlike the old per-server in-memory
// map — every replica reading the same store returns the same snapshot.
func TestUsagePutGet(t *testing.T) {
	s := &apiServer{store: statestore.NewMemoryStore(), token: testToken, clock: fixedClock}
	body := `{"five_hour":{"utilization":16.0},"seven_day":{"utilization":32.0}}`
	if rec := doBody(t, s, http.MethodPut, "/usage/vendor", bearer(), body); rec.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec := do(t, s, http.MethodGet, "/usage", bearer())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"utilization":16`) {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := doBody(t, s, http.MethodPut, "/usage/x", bearer(), "not-json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body status=%d, want 400", rec.Code)
	}
}

// Regression for the replicaCount>1 flicker: a PUT that lands on one gateway replica must be
// visible to a GET on ANOTHER replica. Two apiServers sharing ONE store stand in for two pods —
// with the old per-server in-memory map the second server returned {} half the time.
func TestUsageSharedAcrossReplicas(t *testing.T) {
	store := statestore.NewMemoryStore()
	podA := &apiServer{store: store, token: testToken, clock: fixedClock}
	podB := &apiServer{store: store, token: testToken, clock: fixedClock}
	body := `{"five_hour":{"utilization":28.0}}`
	if rec := doBody(t, podA, http.MethodPut, "/usage/vendor", bearer(), body); rec.Code != http.StatusOK {
		t.Fatalf("put on podA status=%d", rec.Code)
	}
	rec := do(t, podB, http.MethodGet, "/usage", bearer())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"utilization":28`) {
		t.Fatalf("GET on podB status=%d body=%s (want vendor snapshot)", rec.Code, rec.Body.String())
	}
}
