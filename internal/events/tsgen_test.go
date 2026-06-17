package events

import (
	"os"
	"strings"
	"testing"
)

// goldenPath is the checked-in generated TS file the generator must reproduce.
const goldenPath = "types.gen.ts"

// TestGeneratedTypeScriptMatchesGolden pins the codegen: GenerateTypeScript must
// reproduce the checked-in golden byte-for-byte. If the Go taxonomy changes,
// regenerate with `go run ./cmd/eventgen` and commit the updated .ts — otherwise
// this fails, catching Go↔TS drift (ADR-0011 §3 single-source).
func TestGeneratedTypeScriptMatchesGolden(t *testing.T) {
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `go run ./cmd/eventgen`)", goldenPath, err)
	}
	got := GenerateTypeScript()
	if got != string(want) {
		t.Errorf("generated TS differs from %s; regenerate with `go run ./cmd/eventgen`.\n--- got ---\n%s", goldenPath, got)
	}
}

// TestGeneratedTypeScriptCoversTaxonomy guards against a golden that silently
// drops a phase/kind: every taxonomy value must appear as a literal in the
// generated output.
func TestGeneratedTypeScriptCoversTaxonomy(t *testing.T) {
	out := GenerateTypeScript()
	for _, p := range Phases {
		if !strings.Contains(out, "\""+string(p)+"\"") {
			t.Errorf("generated TS missing phase %q", p)
		}
	}
	for _, k := range Kinds {
		if !strings.Contains(out, "\""+string(k)+"\"") {
			t.Errorf("generated TS missing kind %q", k)
		}
	}
}
