package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
)

// maxPixels caps the total pixel count of an input image so a maliciously huge
// PNG cannot exhaust memory in the verify gate (the gate runs over performer
// output). 64 megapixels comfortably covers any real screenshot (e.g. 8K is ~33
// MP) while bounding work to a deterministic ceiling. An oversized image is a
// clear error (exit 2), not a crash.
const maxPixels = 64 * 1024 * 1024

// config is the parsed command line.
type config struct {
	actual    string  // path to the produced/rendered screenshot
	expected  string  // path to the repo-external reference (holdout-injected)
	threshold float64 // max allowed differing-pixel fraction, 0..1 inclusive
}

// Result is the outcome of a Compare: the differing-pixel Fraction (0..1) and
// whether it is Within the requested threshold. It carries NO per-pixel data, so
// nothing about the reference image can leak through it.
type Result struct {
	// Fraction is differing pixels / total pixels, in [0,1].
	Fraction float64
	// Within reports Fraction <= threshold (the PASS decision).
	Within bool
}

// parseArgs parses the positional <actual> <expected> plus -threshold. It is
// strict: exactly two positional paths and a threshold in [0,1] are required, so
// a malformed gate invocation is a clear error rather than a misleading pass.
//
// Flags may appear BEFORE OR AFTER the positional paths so the documented gate
// form `imagediff <actual> <expected> -threshold <frac>` works (Go's flag package
// stops at the first non-flag, so we hoist flag-looking args to the front first).
func parseArgs(args []string) (config, error) {
	fs := flag.NewFlagSet("imagediff", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we print our own usage in run().
	threshold := fs.Float64("threshold", 0, "maximum allowed differing-pixel fraction (0..1)")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return config{}, err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return config{}, fmt.Errorf("need exactly 2 image paths (actual, expected), got %d", len(rest))
	}
	if *threshold < 0 || *threshold > 1 {
		return config{}, fmt.Errorf("threshold %v out of range [0,1]", *threshold)
	}
	return config{actual: rest[0], expected: rest[1], threshold: *threshold}, nil
}

// reorderFlags moves flag tokens (and their values) ahead of positional
// arguments so flag.Parse, which stops at the first non-flag, still sees flags
// given AFTER the positional image paths. It recognizes the single known flag
// in both `-threshold X` and `-threshold=X` forms. Positionals keep their order.
func reorderFlags(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-threshold" || a == "--threshold":
			flags = append(flags, a)
			if i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		case len(a) > 0 && a[0] == '-':
			// Any other flag (incl. -threshold=X) — let flag.Parse handle/err on it.
			flags = append(flags, a)
		default:
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// Compare decodes the two PNGs and returns the differing-pixel Fraction and
// whether it is within threshold. It is the DETERMINISTIC decision core (ADR-0023):
// a pure function of the two files and the threshold, with no I/O beyond reading
// the inputs and no randomness. Errors (missing file, non-PNG/undecodable,
// oversized, or DIMENSION MISMATCH) are returned as errors so the caller maps
// them to a clear non-zero exit — never a silent pass.
//
// Two pixels are "different" if any of their R,G,B,A 8-bit components differ
// (exact match required per component). Anti-aliasing tolerance is expressed
// through the THRESHOLD (allowed differing fraction), keeping the per-pixel test
// itself exact and deterministic.
func Compare(actualPath, expectedPath string, threshold float64) (Result, error) {
	actual, err := decodePNG(actualPath)
	if err != nil {
		return Result{}, fmt.Errorf("actual %q: %w", actualPath, err)
	}
	expected, err := decodePNG(expectedPath)
	if err != nil {
		return Result{}, fmt.Errorf("expected %q: %w", expectedPath, err)
	}

	ab := actual.Bounds()
	eb := expected.Bounds()
	if ab.Dx() != eb.Dx() || ab.Dy() != eb.Dy() {
		return Result{}, fmt.Errorf("dimension mismatch: actual %dx%d vs expected %dx%d", ab.Dx(), ab.Dy(), eb.Dx(), eb.Dy())
	}

	total := ab.Dx() * ab.Dy()
	if total == 0 {
		return Result{}, errors.New("empty image (zero pixels)")
	}

	var diff int
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			ar, ag, abl, aa := actual.At(ab.Min.X+x, ab.Min.Y+y).RGBA()
			er, eg, ebl, ea := expected.At(eb.Min.X+x, eb.Min.Y+y).RGBA()
			if ar != er || ag != eg || abl != ebl || aa != ea {
				diff++
			}
		}
	}

	frac := float64(diff) / float64(total)
	return Result{Fraction: frac, Within: frac <= threshold}, nil
}

// decodePNG opens path and decodes it as PNG, rejecting oversized images before
// the full decode allocates. A missing file, a non-PNG, or an oversized image is
// a clear error (the gate maps it to exit 2).
func decodePNG(path string) (image.Image, error) {
	f, err := os.Open(path) //nolint:gosec // path is the operator-supplied gate argv (actual/reference image).
	if err != nil {
		return nil, err // os error already says "no such file" etc.
	}
	defer func() { _ = f.Close() }()

	// Bound work before a full decode: read the header to learn the dimensions and
	// reject an oversized image deterministically.
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		return nil, fmt.Errorf("decode PNG header: %w", err)
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, fmt.Errorf("image too large: %dx%d exceeds %d-pixel cap", cfg.Width, cfg.Height, maxPixels)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode PNG: %w", err)
	}
	return img, nil
}
