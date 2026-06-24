package agentclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "agent-test-token-xyz"

// newServer builds an httptest server with the given handler and a Client pointed at it.
func newServer(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, testToken), srv
}

func TestLease_Task(t *testing.T) {
	var gotAuth, gotBody string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(b)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"task":{"id":"T-1","project_id":"p","tier":"T2","requires":[],"deps":[]},"lease":{"project_id":"p","host_id":"davinci","task_id":"T-1"}}`))
	})

	lt, ok, err := c.Lease(context.Background(), "p", "davinci", []string{"backend"})
	if err != nil || !ok {
		t.Fatalf("Lease: ok=%v err=%v", ok, err)
	}
	if lt.Task.ID != "T-1" || lt.Lease.HostID != "davinci" {
		t.Fatalf("unexpected leased task: %+v", lt)
	}
	if gotAuth != "Bearer "+testToken {
		t.Fatalf("auth header = %q, want bearer token", gotAuth)
	}
	if !strings.Contains(gotBody, `"host_id":"davinci"`) || !strings.Contains(gotBody, `"backend"`) {
		t.Fatalf("request body missing host/caps: %s", gotBody)
	}
}

func TestGetHoldout(t *testing.T) {
	// 200 → decoded files + name; the ref rides in the query string, auth in the header.
	var gotPath, gotAuth string
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		// "body\n" base64 = "Ym9keQo=".
		_, _ = w.Write([]byte(`{"name":"S-1","files":{"t.ts":"Ym9keQo="}}`))
	})
	name, files, found, err := c.GetHoldout(context.Background(), "pg://holdouts/S-1")
	if err != nil || !found {
		t.Fatalf("GetHoldout: found=%v err=%v", found, err)
	}
	if name != "S-1" || string(files["t.ts"]) != "body\n" {
		t.Fatalf("decoded wrong: name=%q files=%v", name, files)
	}
	if gotAuth != "Bearer "+testToken {
		t.Fatalf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotPath, "/agent/holdout?ref=pg") {
		t.Fatalf("path = %q (ref must be query-escaped)", gotPath)
	}
}

func TestGetHoldout_NotFoundDegrades(t *testing.T) {
	// 404 → found=false, no error (the gate runs without a holdout).
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"holdout not found"}`))
	})
	_, _, found, err := c.GetHoldout(context.Background(), "pg://holdouts/NOPE")
	if err != nil || found {
		t.Fatalf("404 must degrade to found=false,no-error: found=%v err=%v", found, err)
	}
}

func TestGetHoldout_BadBase64Errors(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"X","files":{"a":"!!notb64!!"}}`))
	})
	if _, _, _, err := c.GetHoldout(context.Background(), "pg://holdouts/X"); err == nil {
		t.Fatalf("bad base64 must error")
	}
}

func TestLease_NoWork204(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	_, ok, err := c.Lease(context.Background(), "p", "davinci", nil)
	if err != nil || ok {
		t.Fatalf("204 should be no-work: ok=%v err=%v", ok, err)
	}
}

func TestLease_Raced409(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"raced"}`))
	})
	_, ok, err := c.Lease(context.Background(), "p", "davinci", nil)
	if err != nil || ok {
		t.Fatalf("409 should be no-work (not an error): ok=%v err=%v", ok, err)
	}
}

func TestResult_Decision(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/agent/tasks/T-1/result") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"decision":"hold"}`))
	})
	dec, err := c.Result(context.Background(), "p", "T-1", ResultReport{Result: "pass", Branch: "b"})
	if err != nil || dec != "hold" {
		t.Fatalf("Result decision = %q err=%v, want hold", dec, err)
	}
}

func TestDecision(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"state":"approved"}`))
	})
	st, err := c.Decision(context.Background(), "p", "T-1")
	if err != nil || st != "approved" {
		t.Fatalf("Decision state = %q err=%v, want approved", st, err)
	}
}

func TestScenarios(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"S-1","title":"x","tier":"T2","acceptance":["does a thing"],"hidden_holdout_ref":"store://h/S-1"}]`))
	})
	sc, err := c.Scenarios(context.Background(), "p")
	if err != nil || len(sc) != 1 || sc[0].ID != "S-1" || len(sc[0].Acceptance) != 1 {
		t.Fatalf("Scenarios = %+v err=%v", sc, err)
	}
}

func TestSimpleOK(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	ctx := context.Background()
	if err := c.Heartbeat(ctx, "davinci"); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := c.Merged(ctx, "p", "T-1", "abc"); err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if err := c.Release(ctx, "p", "davinci", "T-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestError_Non2xx_And_TokenNeverLeaks(t *testing.T) {
	c, _ := newServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"host not registered"}`))
	})
	err := c.Heartbeat(context.Background(), "ghost")
	var e *Error
	if !errors.As(err, &e) || e.Status != http.StatusNotFound || e.Message != "host not registered" {
		t.Fatalf("expected *Error 404, got: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error message leaked the token: %s", err.Error())
	}
}
