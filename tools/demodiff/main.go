// Ephemeral local-demo tool (NOT committed): makes a connected Conductor editor show a
// MULTI-FILE native diff for web-shop/T-102. It (1) upserts the FULL-context 2-file patch into
// task_diffs (the P2b full-file source) and (2) publishes a BOUNDED 2-file KindDiff on the bus so
// the editor's live DiffObserver surfaces the diff (the $(git-compare) badge). The user then opens
// it → P2c multi-file diff editor, each file upgraded to the full file by P2b. Run AFTER the editor
// has connected (the /ws subscription is live-only). Reads CONDUCTOR_DSN.
package main

import (
	"context"
	"log"
	"os"

	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/statestore"
)

// fullPatch is the FULL-context 2-file patch (whole-file context) the P2b endpoint serves; the
// editor reconstructs full-file before/after from it for each pane of the multi-file diff editor.
const fullPatch = "diff --git a/api/coupon.go b/api/coupon.go\n--- a/api/coupon.go\n+++ b/api/coupon.go\n@@ -1,16 +1,22 @@\n package api\n \n import \"time\"\n \n // Coupon is a checkout discount.\n type Coupon struct {\n \tCode      string\n \tAmount    int\n+\tPercent   int\n \tExpiresAt time.Time\n }\n \n-func ApplyCoupon(c Coupon, total int) int {\n-\treturn total - c.Amount\n+// ApplyCoupon validates the coupon and returns the discounted total.\n+func ApplyCoupon(c Coupon, total int) (int, error) {\n+\tif c.ExpiresAt.Before(time.Now()) {\n+\t\treturn total, ErrCouponExpired\n+\t}\n+\treturn total - c.Amount, nil\n }\ndiff --git a/api/checkout.go b/api/checkout.go\n--- a/api/checkout.go\n+++ b/api/checkout.go\n@@ -1,9 +1,12 @@\n package api\n \n // Checkout totals a cart, applying an optional coupon.\n-func Checkout(cart []Item) int {\n-\ttotal := Sum(cart)\n-\treturn total\n+func Checkout(cart []Item, coupon *Coupon) int {\n+\ttotal := Sum(cart)\n+\tif coupon != nil {\n+\t\ttotal, _ = ApplyCoupon(*coupon, total)\n+\t}\n+\treturn total\n }\n"

// boundedPatch is the hunk-only patch a real KindDiff event carries (NOTIFY-bounded). The editor
// shows this (→ 2 diffable files → the multi-file diff editor), then P2b upgrades each to fullPatch.
const boundedPatch = "diff --git a/api/coupon.go b/api/coupon.go\n@@ -13,3 +13,9 @@\n-func ApplyCoupon(c Coupon, total int) int {\n-\treturn total - c.Amount\n+func ApplyCoupon(c Coupon, total int) (int, error) {\n+\tif c.ExpiresAt.Before(time.Now()) {\n+\t\treturn total, ErrCouponExpired\n+\t}\n+\treturn total - c.Amount, nil\n }\ndiff --git a/api/checkout.go b/api/checkout.go\n@@ -20,2 +20,5 @@\n-\ttotal := Sum(cart)\n-\treturn total\n+\ttotal := Sum(cart)\n+\tif coupon != nil {\n+\t\ttotal, _ = ApplyCoupon(*coupon, total)\n+\t}\n+\treturn total\n"

func main() {
	dsn := os.Getenv("CONDUCTOR_DSN")
	if dsn == "" {
		log.Fatal("CONDUCTOR_DSN required")
	}
	ctx := context.Background()

	// 1) Upsert the FULL-context 2-file patch (the P2b full-file source served at /tasks/T-102/diff).
	store, err := statestore.NewPostgresStore(ctx, dsn)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer store.Close()
	if err := store.PutTaskDiff(ctx, statestore.TaskDiff{
		ProjectID: "web-shop",
		TaskID:    "T-102",
		Base:      "main",
		Branch:    "conductor/T-102",
		Patch:     fullPatch,
		Truncated: false,
	}); err != nil {
		log.Fatalf("put task diff: %v", err)
	}

	// 2) Publish a BOUNDED 2-file KindDiff so the connected editor's DiffObserver surfaces it.
	bus, err := events.NewPostgresBus(ctx, dsn)
	if err != nil {
		log.Fatalf("bus: %v", err)
	}
	summary := events.DiffSummary{
		Branch: "conductor/T-102",
		Base:   "main",
		Files: []events.DiffFile{
			{Path: "api/coupon.go", Status: "M", Additions: 8, Deletions: 2},
			{Path: "api/checkout.go", Status: "M", Additions: 3, Deletions: 1},
		},
		Patch:     boundedPatch,
		Truncated: false,
	}
	if err := bus.Publish(ctx, events.Event{
		Project: "web-shop",
		Task:    "T-102",
		Phase:   events.PhaseReview,
		Kind:    events.KindDiff,
		Payload: summary.Payload(),
	}); err != nil {
		log.Fatalf("publish: %v", err)
	}
	log.Println("seeded full 2-file diff + published KindDiff for web-shop/T-102")
}
