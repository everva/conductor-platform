package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// writePNG renders a w×h image where pixel (x,y) is decided by fill and writes it
// as PNG to dir/name, returning the path. It is the deterministic fixture
// generator for the diff tests — no external testdata needed for the variable
// cases.
func writePNG(t *testing.T, dir, name string, w, h int, fill func(x, y int) color.Color) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, fill(x, y))
		}
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path) //nolint:gosec // test-controlled temp path.
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	return path
}

var (
	white = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	black = color.RGBA{R: 0, G: 0, B: 0, A: 0xff}
)

func solid(c color.Color) func(x, y int) color.Color {
	return func(_, _ int) color.Color { return c }
}

// TestCompare_Identical: identical images differ by 0 -> within any threshold.
func TestCompare_Identical(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 10, 10, solid(white))
	b := writePNG(t, dir, "b.png", 10, 10, solid(white))
	res, err := Compare(a, b, 0)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Fraction != 0 || !res.Within {
		t.Fatalf("identical images: got fraction=%v within=%v, want 0/true", res.Fraction, res.Within)
	}
}

// TestCompare_BeyondThreshold: many differing pixels with a tiny threshold -> fail.
func TestCompare_BeyondThreshold(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 10, 10, solid(white))
	b := writePNG(t, dir, "b.png", 10, 10, solid(black)) // 100% differ
	res, err := Compare(a, b, 0.5)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Fraction != 1.0 {
		t.Fatalf("all-different: fraction = %v, want 1.0", res.Fraction)
	}
	if res.Within {
		t.Fatalf("100%% diff must exceed 0.5 threshold, got within=true")
	}
}

// TestCompare_WithinThreshold: a small fraction of differing pixels under the
// threshold -> pass. 10 of 100 pixels differ (0.10) with threshold 0.20.
func TestCompare_WithinThreshold(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 10, 10, solid(white))
	// Flip exactly the top row (10 of 100 pixels = 0.10).
	b := writePNG(t, dir, "b.png", 10, 10, func(_, y int) color.Color {
		if y == 0 {
			return black
		}
		return white
	})
	res, err := Compare(a, b, 0.20)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if res.Fraction != 0.10 {
		t.Fatalf("fraction = %v, want 0.10", res.Fraction)
	}
	if !res.Within {
		t.Fatalf("0.10 diff within 0.20 threshold must pass, got within=false")
	}
}

// TestCompare_BoundaryEqualsThreshold: fraction exactly equal to threshold passes
// (<= semantics — the threshold is inclusive).
func TestCompare_BoundaryEqualsThreshold(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 10, 10, solid(white))
	b := writePNG(t, dir, "b.png", 10, 10, func(_, y int) color.Color {
		if y == 0 {
			return black
		}
		return white
	})
	res, err := Compare(a, b, 0.10) // exactly equal
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !res.Within {
		t.Fatalf("fraction == threshold must pass (inclusive), got within=false (frac=%v)", res.Fraction)
	}
}

// TestCompare_DimensionMismatch: differing dimensions -> error (deterministic FAIL).
func TestCompare_DimensionMismatch(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 10, 10, solid(white))
	b := writePNG(t, dir, "b.png", 8, 10, solid(white))
	if _, err := Compare(a, b, 0.5); err == nil {
		t.Fatal("dimension mismatch must error, got nil")
	}
}

// TestCompare_MissingFile: a missing actual/expected file -> error.
func TestCompare_MissingFile(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 4, 4, solid(white))
	if _, err := Compare(a, filepath.Join(dir, "nope.png"), 0.5); err == nil {
		t.Fatal("missing expected file must error, got nil")
	}
	if _, err := Compare(filepath.Join(dir, "nope.png"), a, 0.5); err == nil {
		t.Fatal("missing actual file must error, got nil")
	}
}

// TestCompare_NonPNG: a non-PNG (garbage) file -> decode error.
func TestCompare_NonPNG(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, []byte("not a png"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	good := writePNG(t, dir, "good.png", 4, 4, solid(white))
	if _, err := Compare(bad, good, 0.5); err == nil {
		t.Fatal("non-PNG input must error, got nil")
	}
}

// TestCompare_CommittedTestdata exercises the committed tiny reference PNGs
// (testdata/) the way the web recipe gate does: an actual that MATCHES the
// reference passes; one that DIFFERS fails. These fixtures double as the
// repo-external reference body the holdout injects in the e2e visual demo.
func TestCompare_CommittedTestdata(t *testing.T) {
	ref := filepath.Join("testdata", "reference.png")

	match, err := Compare(filepath.Join("testdata", "match.png"), ref, 0.01)
	if err != nil {
		t.Fatalf("match Compare: %v", err)
	}
	if !match.Within || match.Fraction != 0 {
		t.Fatalf("match.png vs reference.png: frac=%v within=%v, want 0/true", match.Fraction, match.Within)
	}

	mismatch, err := Compare(filepath.Join("testdata", "mismatch.png"), ref, 0.01)
	if err != nil {
		t.Fatalf("mismatch Compare: %v", err)
	}
	if mismatch.Within {
		t.Fatalf("mismatch.png vs reference.png within=true, want false (frac=%v)", mismatch.Fraction)
	}
}

// TestRun_ExitCodes maps the documented exit-code contract end to end through the
// run() wrapper: PASS=0, FAIL=1, error=2.
func TestRun_ExitCodes(t *testing.T) {
	dir := t.TempDir()
	a := writePNG(t, dir, "a.png", 10, 10, solid(white))
	same := writePNG(t, dir, "same.png", 10, 10, solid(white))
	diff := writePNG(t, dir, "diff.png", 10, 10, solid(black))

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"pass identical", []string{a, same, "-threshold", "0"}, exitPass},
		{"fail beyond", []string{a, diff, "-threshold", "0.1"}, exitFail},
		{"error missing", []string{a, filepath.Join(dir, "nope.png"), "-threshold", "0.1"}, exitError},
		{"error no threshold range", []string{a, same, "-threshold", "2"}, exitError},
		{"error wrong arg count", []string{a, "-threshold", "0.1"}, exitError},
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open devnull: %v", err)
	}
	defer func() { _ = devnull.Close() }()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args, devnull, devnull); got != tc.want {
				t.Fatalf("run(%v) = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}
