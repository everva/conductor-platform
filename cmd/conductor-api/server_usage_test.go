package main

import (
	"net/http"
	"strings"
	"testing"
)

// PUT /usage/{key} stores the snapshot; GET /usage returns it. Bad (non-JSON) body is rejected.
func TestUsagePutGet(t *testing.T) {
	s := &apiServer{token: testToken, clock: fixedClock}
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
