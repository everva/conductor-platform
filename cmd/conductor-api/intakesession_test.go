package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestIntakeSessionPutGetRoundTrip: a PUT creates a conversation; GET returns the full thread; a
// second PUT for the same id UPDATES it (upsert), all over the authed director endpoints.
func TestIntakeSessionPutGetRoundTrip(t *testing.T) {
	s, store := emptyServer()
	seedProject(t, store, "p1")

	body := `{"title":"remove service company","messages":[` +
		`{"role":"you","text":"servis şirketini kaldır"},` +
		`{"role":"assistant","text":"need more detail","tone":"warn"}],` +
		`"result":"id: A-1\n","created_at":"2026-06-27T10:00:00Z"}`
	rec := doBody(t, s, http.MethodPut, "/projects/p1/intake/sessions/s1", bearer(), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec = doBody(t, s, http.MethodGet, "/projects/p1/intake/sessions/s1", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got intakeSessionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "s1" || got.ProjectID != "p1" || got.Title != "remove service company" {
		t.Fatalf("GET mapped wrong: %+v", got)
	}
	if len(got.Messages) != 2 || got.Messages[1].Role != "assistant" || got.Messages[1].Tone != "warn" {
		t.Fatalf("messages round-trip wrong: %+v", got.Messages)
	}
	if got.Result != "id: A-1\n" || got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Fatalf("result/timestamps wrong: %+v", got)
	}

	// Upsert: a second PUT REPLACES title + messages for the same id.
	body2 := `{"title":"remove service company (v2)","messages":[{"role":"you","text":"kaldır ve test ekle"}]}`
	rec = doBody(t, s, http.MethodPut, "/projects/p1/intake/sessions/s1", bearer(), body2)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	rec = doBody(t, s, http.MethodGet, "/projects/p1/intake/sessions/s1", bearer(), "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Title != "remove service company (v2)" || len(got.Messages) != 1 {
		t.Fatalf("upsert did not replace content: %+v", got)
	}
}

// TestIntakeSessionListScopedNewestFirst: the list is scoped to a project, ordered newest-first,
// carries no message bodies (light summary), and flags conversations that produced a draft.
func TestIntakeSessionListScopedNewestFirst(t *testing.T) {
	s, store := emptyServer()
	seedProject(t, store, "p1")
	seedProject(t, store, "p2")

	put := func(proj, sid, title, createdAt, result string) {
		body := `{"title":"` + title + `","messages":[{"role":"you","text":"x"}],` +
			`"result":"` + result + `","created_at":"` + createdAt + `"}`
		rec := doBody(t, s, http.MethodPut, "/projects/"+proj+"/intake/sessions/"+sid, bearer(), body)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT %s/%s = %d; body=%s", proj, sid, rec.Code, rec.Body.String())
		}
	}
	put("p1", "s-old", "older", "2026-06-27T10:00:00Z", "")
	put("p1", "s-new", "newer", "2026-06-27T11:00:00Z", "id: E-1")
	put("p2", "s-other", "other proj", "2026-06-27T12:00:00Z", "")

	rec := doBody(t, s, http.MethodGet, "/projects/p1/intake/sessions", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("LIST status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Sessions []intakeSessionSummaryDTO `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(listResp.Sessions) != 2 {
		t.Fatalf("LIST(p1) len = %d, want 2 (p2 must not leak)", len(listResp.Sessions))
	}
	if listResp.Sessions[0].ID != "s-new" || listResp.Sessions[1].ID != "s-old" {
		t.Fatalf("LIST order = [%s,%s], want [s-new,s-old]", listResp.Sessions[0].ID, listResp.Sessions[1].ID)
	}
	if !listResp.Sessions[0].HasResult || listResp.Sessions[1].HasResult {
		t.Fatalf("has_result wrong: s-new=%v s-old=%v", listResp.Sessions[0].HasResult, listResp.Sessions[1].HasResult)
	}
}

// TestIntakeSessionCrossProjectAndMissing404: a session is only readable through its OWN project,
// and a missing id is a 404.
func TestIntakeSessionCrossProjectAndMissing404(t *testing.T) {
	s, store := emptyServer()
	seedProject(t, store, "p1")
	seedProject(t, store, "p2")
	doBody(t, s, http.MethodPut, "/projects/p1/intake/sessions/s1", bearer(),
		`{"title":"t","messages":[{"role":"you","text":"x"}]}`)

	// Right id, WRONG project → 404 (pairing guard).
	if rec := doBody(t, s, http.MethodGet, "/projects/p2/intake/sessions/s1", bearer(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project GET = %d, want 404", rec.Code)
	}
	// Missing id → 404.
	if rec := doBody(t, s, http.MethodGet, "/projects/p1/intake/sessions/nope", bearer(), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing GET = %d, want 404", rec.Code)
	}
}

// TestIntakeSessionPutMissingProject404: persisting into a non-existent project is rejected.
func TestIntakeSessionPutMissingProject404(t *testing.T) {
	s, _ := emptyServer()
	rec := doBody(t, s, http.MethodPut, "/projects/ghost/intake/sessions/s1", bearer(),
		`{"title":"t","messages":[{"role":"you","text":"x"}]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT missing project = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestIntakeSessionRequiresAuth: every intake-session route is behind requireAuth (401 unauthed).
func TestIntakeSessionRequiresAuth(t *testing.T) {
	s, store := emptyServer()
	seedProject(t, store, "p1")
	cases := []struct {
		method, path string
	}{
		{http.MethodPut, "/projects/p1/intake/sessions/s1"},
		{http.MethodGet, "/projects/p1/intake/sessions"},
		{http.MethodGet, "/projects/p1/intake/sessions/s1"},
	}
	for _, c := range cases {
		if rec := doBody(t, s, c.method, c.path, "", `{}`); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s unauthed = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}
