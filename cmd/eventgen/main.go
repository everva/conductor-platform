// Command eventgen generates the TypeScript event-taxonomy types from the Go
// single source (internal/events) per ADR-0011 §3. It writes the result to the
// path given by -out (default the checked-in golden), so regenerating after a
// taxonomy change updates the TS types in lockstep. The golden-file test
// (internal/events.TestGeneratedTypeScriptMatchesGolden) fails if the checked-in
// file drifts from the generator, catching any Go↔TS divergence in CI.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/everva/conductor-platform/internal/events"
)

func main() {
	out := flag.String("out", "internal/events/types.gen.ts",
		"path to write the generated TypeScript types")
	flag.Parse()

	src := events.GenerateTypeScript()
	if err := os.WriteFile(*out, []byte(src), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "eventgen: write %s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "eventgen: wrote %s (%d bytes)\n", *out, len(src))
}
