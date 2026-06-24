package agent

import (
	"context"
	"errors"
	"testing"
)

// TestGatewayHoldout_Fetch pins the adapter that turns a gateway holdout-fetch into a
// verify.HoldoutStore (Faz-S S3): empty ref / not-found → empty holdout (gate runs public-only);
// found → the injectable files; an error propagates.
func TestGatewayHoldout_Fetch(t *testing.T) {
	ctx := context.Background()

	// Empty ref → no fetch, empty holdout.
	called := false
	g := NewGatewayHoldout(func(context.Context, string) (string, map[string][]byte, bool, error) {
		called = true
		return "", nil, false, nil
	})
	h, err := g.Fetch(ctx, "  ")
	if err != nil || len(h.Files) != 0 || called {
		t.Fatalf("empty ref must short-circuit: err=%v files=%d called=%v", err, len(h.Files), called)
	}

	// Found → name + files become the holdout.
	g = NewGatewayHoldout(func(_ context.Context, ref string) (string, map[string][]byte, bool, error) {
		if ref != "pg://holdouts/S-1" {
			t.Fatalf("ref = %q", ref)
		}
		return "S-1", map[string][]byte{"t.ts": []byte("body")}, true, nil
	})
	h, err = g.Fetch(ctx, "pg://holdouts/S-1")
	if err != nil || h.Name != "S-1" || string(h.Files["t.ts"]) != "body" {
		t.Fatalf("found mapping failed: %+v err=%v", h, err)
	}

	// Not found → empty holdout (public gates still run).
	g = NewGatewayHoldout(func(context.Context, string) (string, map[string][]byte, bool, error) {
		return "", nil, false, nil
	})
	if h, err = g.Fetch(ctx, "pg://holdouts/X"); err != nil || len(h.Files) != 0 {
		t.Fatalf("not-found must yield empty holdout: %+v err=%v", h, err)
	}

	// Error → propagate (a real fetch failure must not silently pass the gate).
	g = NewGatewayHoldout(func(context.Context, string) (string, map[string][]byte, bool, error) {
		return "", nil, false, errors.New("gateway down")
	})
	if _, err = g.Fetch(ctx, "pg://holdouts/Y"); err == nil {
		t.Fatalf("fetch error must propagate")
	}
}
