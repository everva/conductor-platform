// Command imagediff is the DETERMINISTIC core of the web visual-verify recipe
// gate (ADR-0023): a tiny, dependency-free PNG pixel-diff. It is invoked as a
// recipe GATE (verify.Gate{Argv}) — NOT a new verify-type — and turns a
// render→diff comparison into a single deterministic exit code:
//
//	imagediff <actual.png> <expected.png> -threshold <frac>
//	  exit 0  -> the images differ by AT MOST <frac> of their pixels (PASS)
//	  exit 1  -> they differ by MORE than <frac> (FAIL) — a non-revealing summary
//	  exit 2  -> a usage/IO/decoding/dimension error (clear, deterministic FAIL)
//
// It uses only the standard library (image, image/png): no browser, no network,
// no new Go dependency. The DECISION is therefore offline-verifiable and
// reproducible regardless of whether the optional render front-end (Playwright)
// is installed — a missing render tool yields a missing-actual-file error here,
// i.e. a deterministic FAIL, never a silent skip (ADR-0023, Rule#9).
//
// The summary printed on FAIL is intentionally NON-REVEALING: it reports only the
// differing-pixel FRACTION versus the threshold, never which pixels or colors
// differ, so a performer cannot reverse-engineer the hidden reference image.
package main

import (
	"fmt"
	"os"
)

// exit codes (documented in the package comment): they are the gate contract.
const (
	exitPass  = 0 // within threshold
	exitFail  = 1 // beyond threshold
	exitError = 2 // usage / IO / decode / dimension error
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main split out for testing: it parses args, runs the diff, and returns
// the process exit code. stdout carries the PASS/FAIL summary; stderr carries
// usage/IO errors. It never panics on bad input — every error path returns
// exitError with a clear message.
func run(args []string, stdout, stderr *os.File) int {
	cfg, err := parseArgs(args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "imagediff: %v\n", err)
		_, _ = fmt.Fprintf(stderr, "usage: imagediff <actual.png> <expected.png> -threshold <frac 0..1>\n")
		return exitError
	}

	res, err := Compare(cfg.actual, cfg.expected, cfg.threshold)
	if err != nil {
		// Missing/oversized/dimension-mismatch/decode error -> deterministic FAIL
		// via a clear error (exit 2), never a silent skip (ADR-0023).
		_, _ = fmt.Fprintf(stderr, "imagediff: %v\n", err)
		return exitError
	}

	if res.Within {
		_, _ = fmt.Fprintf(stdout, "visual-diff PASS: differing fraction %.6f <= threshold %.6f\n", res.Fraction, cfg.threshold)
		return exitPass
	}
	// NON-REVEALING summary: fraction vs threshold only — never which pixels.
	_, _ = fmt.Fprintf(stdout, "visual-diff FAIL: differing fraction %.6f > threshold %.6f\n", res.Fraction, cfg.threshold)
	return exitFail
}
