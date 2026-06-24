package intake

import (
	"strings"
	"testing"
)

const holdoutStdout = "narration...\n" +
	"<<<SCENARIOS\n" +
	"scenarios:\n" +
	"  - id: A-1\n" +
	"    title: healthz\n" +
	"    lane: web\n" +
	"    tier: T2\n" +
	"    acceptance:\n" +
	"      - GET /healthz is 200\n" +
	"    hidden_holdout_ref: \"pg://holdouts/A-1\"\n" +
	"SCENARIOS>>>\n" +
	"more narration...\n" +
	"<<<HOLDOUT\n" +
	"files:\n" +
	"  \"healthz.spec.ts\": |\n" +
	"    import { test, expect } from '@playwright/test'\n" +
	"    test('healthz', async ({ page }) => { /* ... */ })\n" +
	"HOLDOUT>>>\n"

func TestParseHoldout(t *testing.T) {
	files, ok := ParseHoldout([]byte(holdoutStdout))
	if !ok {
		t.Fatal("expected a holdout block")
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	body := files["healthz.spec.ts"]
	if body == "" || !strings.Contains(body, "@playwright/test") {
		t.Fatalf("holdout content wrong: %q", body)
	}

	// Absent block → (nil,false), NOT an error.
	if _, ok := ParseHoldout([]byte("just prose, no fences")); ok {
		t.Fatal("absent holdout must return ok=false")
	}
	// Malformed YAML inside the block → (nil,false).
	if _, ok := ParseHoldout([]byte("<<<HOLDOUT\n: : not yaml\nHOLDOUT>>>")); ok {
		t.Fatal("malformed holdout must return ok=false")
	}
	// Empty files map → (nil,false).
	if _, ok := ParseHoldout([]byte("<<<HOLDOUT\nfiles: {}\nHOLDOUT>>>")); ok {
		t.Fatal("empty files must return ok=false")
	}
}

func TestParseOutcome_AttachesHoldout(t *testing.T) {
	out, err := ParseOutcome([]byte(holdoutStdout))
	if err != nil {
		t.Fatalf("ParseOutcome: %v", err)
	}
	if len(out.Scenarios) != 1 || out.Scenarios[0].ID != "A-1" {
		t.Fatalf("scenarios wrong: %+v", out.Scenarios)
	}
	if len(out.Holdout) != 1 || out.Holdout["healthz.spec.ts"] == "" {
		t.Fatalf("outcome must carry the auto-generated holdout: %v", out.Holdout)
	}
}
