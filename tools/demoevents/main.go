// Ephemeral local-demo seeder (NOT committed): publishes a realistic lifecycle of events for the
// demo gateway's web-shop tasks so the Faz-Q native Events panel (Q2) + the SessionView timeline/
// verdict/diff (Q4.1) show REAL data instead of empty welcomes. Persists via the PG bus (events
// table + NOTIFY) so the editor's REST backfill surfaces them on connect. Reads CONDUCTOR_DSN.
package main

import (
	"context"
	"log"
	"os"

	"github.com/everva/conductor-platform/internal/events"
)

func diffPayload(path, patch string, add, del int) map[string]any {
	return events.DiffSummary{
		Branch:    "conductor/x",
		Base:      "main",
		Files:     []events.DiffFile{{Path: path, Status: "M", Additions: add, Deletions: del}},
		Patch:     patch,
		Truncated: false,
	}.Payload()
}

func decisionPayload(result string) map[string]any {
	return map[string]any{
		"result": result,
		"checks": []any{
			map[string]any{"name": "go build", "result": "pass", "evidence": "exit 0"},
			map[string]any{"name": "unit tests", "result": "pass", "evidence": "ok (12 tests)"},
			map[string]any{"name": "hidden holdout", "result": result, "evidence": "exit 0"},
		},
	}
}

func main() {
	dsn := os.Getenv("CONDUCTOR_DSN")
	if dsn == "" {
		log.Fatal("CONDUCTOR_DSN required")
	}
	ctx := context.Background()
	bus, err := events.NewPostgresBus(ctx, dsn)
	if err != nil {
		log.Fatalf("bus: %v", err)
	}

	type ev struct {
		task    string
		phase   events.Phase
		kind    events.Kind
		payload map[string]any
	}
	couponPatch := "@@ -13,3 +13,7 @@\n-func ApplyCoupon(c Coupon, total int) int {\n-\treturn total - c.Amount\n+func ApplyCoupon(c Coupon, total int) (int, error) {\n+\tif c.ExpiresAt.Before(time.Now()) {\n+\t\treturn total, ErrCouponExpired\n+\t}\n+\treturn total - c.Amount, nil\n }\n"
	cartPatch := "@@ -1,4 +1,6 @@\n func CartTotal(items []Item) int {\n-\treturn sum(items)\n+\tt := sum(items)\n+\treturn applyTax(t)\n }\n"

	// Lifecycle in publish order → increasing timestamps → correct timeline + newest-first feed.
	seq := []ev{
		// T-103 (done): full develop→verify→verdict→diff→merge run.
		{"T-103", events.PhaseDevelop, events.KindStarted, map[string]any{"message": "agent started on conductor/T-103"}},
		{"T-103", events.PhaseDevelop, events.KindProgress, map[string]any{"pct": 70}},
		{"T-103", events.PhaseVerify, events.KindStarted, map[string]any{"message": "running the deterministic gate"}},
		{"T-103", events.PhaseReview, events.KindDecision, decisionPayload("pass")},
		{"T-103", events.PhaseReview, events.KindDiff, diffPayload("api/cart.go", cartPatch, 2, 1)},
		{"T-103", events.PhaseMerge, events.KindMerge, map[string]any{"sha": "9f2c1ab", "base": "main"}},
		// T-101 (running): in-flight.
		{"T-101", events.PhaseDevelop, events.KindStarted, map[string]any{"message": "agent started on conductor/T-101"}},
		{"T-101", events.PhaseDevelop, events.KindProgress, map[string]any{"pct": 35}},
		{"T-101", events.PhaseDevelop, events.KindLog, map[string]any{"line": "editing src/checkout/page.tsx"}},
		// T-102 (awaiting-approval): gate passed, held for the director.
		{"T-102", events.PhaseDevelop, events.KindStarted, map[string]any{"message": "agent started on conductor/T-102"}},
		{"T-102", events.PhaseVerify, events.KindStarted, map[string]any{"message": "running the deterministic gate"}},
		{"T-102", events.PhaseReview, events.KindDecision, decisionPayload("pass")},
		{"T-102", events.PhaseReview, events.KindDiff, diffPayload("api/coupon.go", couponPatch, 5, 2)},
	}

	for _, e := range seq {
		if err := bus.Publish(ctx, events.Event{
			Project: "web-shop",
			Task:    e.task,
			Phase:   e.phase,
			Kind:    e.kind,
			Payload: e.payload,
		}); err != nil {
			log.Fatalf("publish %s/%s/%s: %v", e.task, e.phase, e.kind, err)
		}
	}
	log.Printf("seeded %d events for web-shop (T-101/T-102/T-103)", len(seq))
}
