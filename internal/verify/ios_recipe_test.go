package verify

// iOS/mobile maestro recipe gate proof (task 2A-3). These tests exercise the REAL
// iOS profile gates (scaffolder.ProfileFor(StackIOS)) through runGate — the exact
// command-gate path verify uses — to prove OFFLINE the deterministic halves of the
// iOS recipe, since Xcode/maestro/simulators are NOT available here:
//
//	(b) the iOS recipe's VISUAL gate behaves IDENTICALLY to the web one — it is the
//	    SAME imagediff command, so it PASSes on a matching screenshot and FAILs on a
//	    differing one (ADR-0023 gate reuse).
//	(c) a MISSING maestro / xcodebuild binary makes its gate FAIL deterministically
//	    (missing-binary, fix-#1 pattern) — never a silent skip (Rule#9).
//
// What is DEFERRED to Dalga B (a real Mac host) and is NOT proven here: the LIVE
// `xcodebuild test` compile/run and the LIVE `maestro test` driving an iOS
// simulator (which would PRODUCE the screenshot the visual gate then diffs). Here
// the maestro/xcodebuild legs are absent-binary FAILs by design; only the
// deterministic visual DECISION is run live (via the built imagediff binary).

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/everva/conductor-platform/internal/scaffolder"
)

// TestIOSRecipe_MissingMaestroAndXcodebuild_FailDeterministically proves the iOS
// recipe's maestro and xcodebuild gates FAIL (not skip) when their operator-
// provided binaries are absent — the missing-binary / fix-#1 deterministic FAIL
// (Rule#9). It runs the ACTUAL argv from the iOS profile, with PATH emptied so the
// binaries cannot resolve even if a Mac dev box happens to have them.
func TestIOSRecipe_MissingMaestroAndXcodebuild_FailDeterministically(t *testing.T) {
	p, ok := scaffolder.ProfileFor(scaffolder.StackIOS)
	if !ok {
		t.Fatal("scaffolder.ProfileFor(ios) not found")
	}
	// Empty PATH so maestro/xcodebuild cannot resolve regardless of the host.
	t.Setenv("PATH", "")
	dir := t.TempDir()

	cases := []struct {
		name string
		argv []string
	}{
		{"maestro", p.Maestro},
		{"xcodebuild-build", p.Build},
		{"xcodebuild-test", p.Test},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.argv) == 0 {
				t.Fatalf("iOS profile %s gate is empty", tc.name)
			}
			c := runGate(context.Background(), dir, Gate{Name: tc.name, Argv: tc.argv})
			if c.Result != checkFail {
				t.Fatalf("%s gate result = %q, want %q (missing binary must FAIL, never skip)", tc.name, c.Result, checkFail)
			}
			if c.Evidence == "" {
				t.Fatalf("%s gate evidence empty; want the exec error surfaced for diagnosis", tc.name)
			}
		})
	}
}

// TestIOSRecipe_VisualGate_ReusesImagediffPassFail proves the iOS recipe's VISUAL
// gate is the SAME deterministic imagediff decision as the web recipe (ADR-0023
// reuse): byte-identical argv, PASS on a matching screenshot, FAIL on a differing
// one. The maestro render leg that PRODUCES the screenshot is Dalga-B (Mac); here
// we supply the actual.png directly to prove the gate DECISION offline.
func TestIOSRecipe_VisualGate_ReusesImagediffPassFail(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build imagediff")
	}
	ios, _ := scaffolder.ProfileFor(scaffolder.StackIOS)
	web, _ := scaffolder.ProfileFor(scaffolder.StackWeb)
	// Gate reuse: the iOS visual argv is identical to the web one (same imagediff).
	if len(ios.Visual) == 0 || len(web.Visual) == 0 {
		t.Fatalf("expected both iOS and web visual gates; ios=%v web=%v", ios.Visual, web.Visual)
	}
	for i := range web.Visual {
		if i >= len(ios.Visual) || ios.Visual[i] != web.Visual[i] {
			t.Fatalf("iOS visual gate argv differs from web: ios=%v web=%v", ios.Visual, web.Visual)
		}
	}

	// Build the real imagediff onto PATH so the gate runs the actual tool.
	binDir := t.TempDir()
	out := filepath.Join(binDir, "imagediff")
	build := exec.Command("go", "build", "-o", out, "github.com/everva/conductor-platform/cmd/imagediff")
	var bo bytes.Buffer
	build.Stdout = &bo
	build.Stderr = &bo
	if err := build.Run(); err != nil {
		t.Fatalf("build imagediff: %v\n%s", err, bo.String())
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Lay the reference and actual images at the iOS recipe's convention paths.
	dir := t.TempDir()
	visualDir := filepath.Join(dir, filepath.Dir(scaffolder.VisualReferencePath))
	if err := os.MkdirAll(visualDir, 0o755); err != nil {
		t.Fatalf("mkdir visual: %v", err)
	}
	ref := solidPNG(t, 16, 16, color.RGBA{R: 0x20, G: 0x80, B: 0xff, A: 0xff})
	if err := os.WriteFile(filepath.Join(dir, scaffolder.VisualReferencePath), ref, 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}

	gate := Gate{Name: "visual", Argv: ios.Visual}

	// PASS: actual byte-identical to the reference -> imagediff exit 0.
	if err := os.WriteFile(filepath.Join(dir, scaffolder.VisualActualPath), ref, 0o644); err != nil {
		t.Fatalf("write actual (match): %v", err)
	}
	if c := runGate(context.Background(), dir, gate); c.Result != checkPass {
		t.Fatalf("matching screenshot: visual gate = %q (%s), want %q", c.Result, c.Evidence, checkPass)
	}

	// FAIL: a wholly different actual -> 100%% differing pixels -> exit non-0.
	diff := solidPNG(t, 16, 16, color.RGBA{R: 0xff, G: 0x00, B: 0x00, A: 0xff})
	if err := os.WriteFile(filepath.Join(dir, scaffolder.VisualActualPath), diff, 0o644); err != nil {
		t.Fatalf("write actual (differ): %v", err)
	}
	if c := runGate(context.Background(), dir, gate); c.Result != checkFail {
		t.Fatalf("differing screenshot: visual gate = %q (%s), want %q", c.Result, c.Evidence, checkFail)
	}
}

// TestIOSRecipe_VisualGate_MissingScreenshotFails proves the iOS visual gate FAILS
// deterministically when the maestro render leg produced NO screenshot (the
// actual.png is absent) — imagediff reports a clear error (exit 2), never a silent
// skip. This is the iOS analogue of the web "missing actual" rule (ADR-0023).
func TestIOSRecipe_VisualGate_MissingScreenshotFails(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available to build imagediff")
	}
	ios, _ := scaffolder.ProfileFor(scaffolder.StackIOS)

	binDir := t.TempDir()
	out := filepath.Join(binDir, "imagediff")
	build := exec.Command("go", "build", "-o", out, "github.com/everva/conductor-platform/cmd/imagediff")
	var bo bytes.Buffer
	build.Stdout = &bo
	build.Stderr = &bo
	if err := build.Run(); err != nil {
		t.Fatalf("build imagediff: %v\n%s", err, bo.String())
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	visualDir := filepath.Join(dir, filepath.Dir(scaffolder.VisualReferencePath))
	if err := os.MkdirAll(visualDir, 0o755); err != nil {
		t.Fatalf("mkdir visual: %v", err)
	}
	ref := solidPNG(t, 16, 16, color.RGBA{R: 0x20, G: 0x80, B: 0xff, A: 0xff})
	if err := os.WriteFile(filepath.Join(dir, scaffolder.VisualReferencePath), ref, 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	// NO actual.png written (render leg deferred to Dalga B / produced nothing).
	c := runGate(context.Background(), dir, Gate{Name: "visual", Argv: ios.Visual})
	if c.Result != checkFail {
		t.Fatalf("missing screenshot: visual gate = %q (%s), want %q (no silent skip)", c.Result, c.Evidence, checkFail)
	}
}

// solidPNG returns a w×h solid-color PNG as bytes (a deterministic test image).
func solidPNG(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
