// Package scaffolder onboards a config-less repository (ADR-0009): it detects the
// repo's stack from marker files, selects a per-stack recipe profile (the
// build/test/vet/lint gate commands the CommandEngine and verify gate consume,
// ADR-0002/ADR-0003), drafts a `.conductor/` config, and enforces the
// readiness-gate.
//
// The readiness-gate is the load-bearing part: the whole platform depends on a
// deterministic test/build gate (ADR-0003), so a repo with NO test
// infrastructure is reported NOT-READY with an actionable reason — its first task
// must be "set up quality infrastructure" before any autonomous feature task
// runs (ADR-0009 §Readiness-gate).
//
// Everything here is deterministic and offline: detection, profile selection,
// readiness, and draft generation are pure functions of the files on disk. Human
// approval of the draft (the "assisted" half of ADR-0009) is interactive and out
// of scope for this package.
package scaffolder

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Stack is the detected primary technology of a repo. The zero value is
// StackUnknown so an undetected repo never silently looks like a real stack.
type Stack string

// The supported stacks. StackUnknown is returned when no marker file matches; it
// is the deliberate "I cannot onboard this" signal, not an error.
const (
	StackUnknown Stack = "unknown"
	StackGo      Stack = "go"
	StackNode    Stack = "node"
	StackPython  Stack = "python"
	StackRust    Stack = "rust"
	// StackWeb is a Node project that ALSO carries a Playwright config: a web
	// front-end whose recipe adds the deterministic visual-diff gate (ADR-0023).
	// It is detected ahead of plain Node so a Playwright web app gets the visual
	// recipe rather than the generic Node one.
	StackWeb Stack = "web"
	// StackIOS is a mobile/iOS project driven by maestro UI-flows. Its recipe
	// mirrors the web one (ADR-0023): a build/test leg (xcodebuild/swift test, run
	// LIVE on a Mac host in Dalga B), a maestro UI-flow gate, and the SAME
	// deterministic visual-diff gate (imagediff) the web recipe uses. The
	// iOS-build capability hook (`requires: ios-build`) routes its live run to a
	// Mac host (ADR-0008, routing itself is Dalga-B 2B-2). It is detected ahead of
	// plain stacks so a maestro/Xcode project gets the mobile recipe.
	StackIOS Stack = "ios"
)

// String returns the stack identifier (its underlying string), so a Stack prints
// as "go"/"node"/… in logs and the generated draft.
func (s Stack) String() string { return string(s) }

// detectionRule is one entry in the ordered marker table. A rule matches when ANY
// of its markers is present in the repo root. A marker is either an exact name
// (file OR directory, see markerExists) or a glob pattern (e.g. "*.xcodeproj")
// matched against the directory's entries — the glob support lets the iOS rule key
// off Xcode bundles whose names vary per project.
type detectionRule struct {
	stack   Stack
	markers []string
	// globs are filepath.Match patterns matched against the repo root's entries
	// (file OR directory). Used for variably-named markers like "*.xcodeproj".
	globs []string
}

// detectionRules is the ordered marker table that drives DetectStack. Order is
// significant and defines primary-stack precedence for multi-stack repos: earlier
// rules win. The order is deliberate, not alphabetical — see DetectStack.
var detectionRules = []detectionRule{
	{stack: StackGo, markers: []string{"go.mod"}},
	{stack: StackRust, markers: []string{"Cargo.toml"}},
	{stack: StackPython, markers: []string{"pyproject.toml", "requirements.txt", "setup.py", "setup.cfg"}},
	// iOS/mobile (maestro-driven) is probed BEFORE web/Node so a maestro+Xcode
	// project gets the mobile recipe (maestro UI-flow + visual gate, ADR-0023)
	// rather than a generic stack. Markers (any one matches): a `maestro/` flows
	// directory, an Xcode project/workspace bundle (`*.xcodeproj`/`*.xcworkspace`),
	// or an iOS-app `Project.swift` (Tuist). These are clean, documented mobile
	// markers; a plain `Package.swift` library is NOT treated as iOS.
	{stack: StackIOS, markers: []string{"maestro", "Project.swift"}, globs: []string{"*.xcodeproj", "*.xcworkspace"}},
	// Web (Node + Playwright) is probed BEFORE plain Node so a Playwright web app
	// gets the visual-diff recipe (ADR-0023). Its markers are the Playwright config
	// files; a package.json without one stays plain Node.
	{stack: StackWeb, markers: []string{"playwright.config.ts", "playwright.config.js", "playwright.config.mjs"}},
	{stack: StackNode, markers: []string{"package.json"}},
}

// Detection is the deterministic result of inspecting a repo directory: the
// chosen Primary stack plus EVERY stack whose markers were present (All), in
// detection-rule order. A polyglot repo surfaces all of its stacks so the caller
// can warn, while Primary stays deterministic.
type Detection struct {
	// Primary is the stack the profile/readiness logic uses; StackUnknown if none.
	Primary Stack
	// All lists every detected stack in precedence order; empty if unknown.
	All []Stack
}

// DetectStack inspects dir for marker files and returns a Detection. It is
// deterministic: it walks detectionRules in their fixed precedence order, so the
// same directory always yields the same Primary regardless of filesystem
// ordering.
//
// Multi-stack handling: a repo with several stacks' markers (e.g. a Go service
// with a Node frontend) reports the FIRST matching rule as Primary and lists the
// rest in All. The chosen precedence (Go > Rust > Python > Node) puts the
// compiled/backend stack first because that is typically where the platform's
// autonomous gate runs; callers that disagree can read All and override.
//
// A missing or unreadable directory yields a StackUnknown Detection (no error):
// "cannot detect" is a normal, reportable verdict, not a failure.
func DetectStack(dir string) Detection {
	var all []Stack
	for _, rule := range detectionRules {
		if anyFileExists(dir, rule.markers) || anyDirExists(dir, rule.markers) || anyGlobMatches(dir, rule.globs) {
			all = append(all, rule.stack)
		}
	}
	if len(all) == 0 {
		return Detection{Primary: StackUnknown}
	}
	return Detection{Primary: all[0], All: all}
}

// anyFileExists reports whether any of names exists as a regular file directly in
// dir. It ignores directories so a stray `package.json/` dir cannot be mistaken
// for a marker.
func anyFileExists(dir string, names []string) bool {
	for _, name := range names {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// anyDirExists reports whether any of names exists as a DIRECTORY directly in dir.
// It complements anyFileExists for markers that are directories (e.g. iOS's
// `maestro/` flows dir), so a directory marker is not silently missed.
func anyDirExists(dir string, names []string) bool {
	for _, name := range names {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// anyGlobMatches reports whether any of the filepath.Match patterns matches an
// entry (file OR directory) directly in dir. It backs variably-named markers like
// `*.xcodeproj`/`*.xcworkspace` whose exact name is project-specific. A malformed
// pattern is ignored (treated as no match), never panicking detection.
func anyGlobMatches(dir string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, pattern := range patterns {
		for _, e := range entries {
			if ok, merr := filepath.Match(pattern, e.Name()); merr == nil && ok {
				return true
			}
		}
	}
	return false
}

// Profile is the per-stack recipe template ADR-0009 selects: the deterministic
// gate commands the verify step runs (ADR-0003) and the develop command the
// CommandEngine drives (ADR-0002). Each command is an argv slice (program +
// args), matching engine.RecipeConfig / verify.Gate which execute without a
// shell.
type Profile struct {
	// Stack is the stack this profile serves.
	Stack Stack
	// Build is the argv that compiles/assembles the project (gate command).
	Build []string
	// Test is the argv that runs the test suite (gate + readiness anchor).
	Test []string
	// Vet is the argv for the stack's vet/static-analysis equivalent (gate).
	Vet []string
	// Lint is the argv for the stack's linter (gate).
	Lint []string
	// Maestro is the OPTIONAL maestro UI-flow gate argv (2A-3): it runs a maestro
	// test flow (`maestro test <flow>`) whose exit code is the gate verdict — a
	// command gate like any other. Empty for stacks without a UI-flow recipe; set
	// for StackIOS. The maestro binary is operator-provided; if absent the gate
	// FAILS deterministically (missing-binary, fix-#1 pattern) — never a silent
	// skip. On a Mac host this drives a simulator (LIVE in Dalga B); the gate
	// DEFINITION here is offline-verifiable (round-trip + missing-binary FAIL).
	Maestro []string
	// Visual is the OPTIONAL deterministic visual-diff gate argv (ADR-0023): it
	// renders/produces a screenshot and runs the imagediff tool against a
	// repo-external reference (holdout-injected) at a threshold. Empty for stacks
	// without a visual recipe (Go/Node/Python/Rust); set for StackWeb and StackIOS.
	// It runs AFTER the standard gates so a build/test/lint failure surfaces first.
	Visual []string
	// Capability is the OPTIONAL host capability this stack's live gate run
	// REQUIRES (ADR-0008 lane `requires`). Empty for stacks runnable on any host;
	// "ios-build" for StackIOS so Dalga-B capability routing (2B-2) sends its live
	// xcodebuild/maestro-on-simulator run to a Mac host. It is the routing HOOK;
	// the routing itself is Dalga B.
	Capability string
}

// Gates returns the profile's gate commands in deterministic order
// (build, test, vet, lint, maestro, visual), each as an argv slice, skipping any
// that are empty. This is the shape verify consumes (one Gate per command). The
// maestro UI-flow (2A-3) and visual-diff (ADR-0023) gates run LAST so a
// build/test/lint failure surfaces before them.
func (p Profile) Gates() [][]string {
	candidates := [][]string{p.Build, p.Test, p.Vet, p.Lint, p.Maestro, p.Visual}
	gates := make([][]string, 0, len(candidates))
	for _, c := range candidates {
		if len(c) > 0 {
			gates = append(gates, c)
		}
	}
	return gates
}

// The web visual-diff recipe paths/threshold (ADR-0023). These are the
// CONVENTION the web profile, the render step, and the holdout reference all
// agree on, so the deterministic gate finds both images in the verify-worktree:
//
//   - VisualActualPath:    where the render step (Playwright) writes the produced
//     screenshot — relative to the repo root.
//   - VisualReferencePath: where the repo-EXTERNAL reference image is injected by
//     the holdout (holdout inject/-relative path maps here). The performer never
//     authors this file, so it cannot overfit the reference (ADR-0018).
//   - DefaultVisualThreshold: the max differing-pixel fraction the gate tolerates.
const (
	VisualActualPath       = ".conductor/visual/actual.png"
	VisualReferencePath    = ".conductor/visual/reference.png"
	DefaultVisualThreshold = "0.02"
)

// The iOS/mobile maestro recipe conventions (2A-3). They mirror the web visual
// recipe so the iOS profile reuses the SAME deterministic visual gate (imagediff):
//
//   - MaestroFlowPath: the maestro UI-flow file the maestro gate runs. The render
//     leg (`maestro test`) drives a simulator on a Mac host (LIVE in Dalga B) and
//     can capture the screenshot the visual gate then diffs at VisualActualPath.
//   - CapabilityIOSBuild: the host capability the iOS lane REQUIRES (ADR-0008
//     `requires`), so Dalga-B routing (2B-2) sends the live build/maestro run to a
//     Mac host. It is the routing hook; the routing itself is Dalga B.
const (
	MaestroFlowPath    = "maestro/flow.yaml"
	CapabilityIOSBuild = "ios-build"
)

// profiles is the data-driven per-stack registry (ADR-0009 per-stack profile
// library). Adding a stack is a data edit here plus a detection rule above and a
// readiness probe below — no control-flow changes.
var profiles = map[Stack]Profile{
	StackGo: {
		Stack: StackGo,
		Build: []string{"go", "build", "./..."},
		Test:  []string{"go", "test", "./..."},
		Vet:   []string{"go", "vet", "./..."},
		Lint:  []string{"golangci-lint", "run"},
	},
	StackNode: {
		Stack: StackNode,
		Build: []string{"npm", "run", "build"},
		Test:  []string{"npm", "test"},
		Vet:   []string{"npm", "run", "typecheck"},
		Lint:  []string{"npx", "eslint", "."},
	},
	// StackWeb is Node + the deterministic visual-diff gate (ADR-0023). The render
	// front-end (Playwright) produces a screenshot at VisualActualPath; the
	// repo-external reference is holdout-injected at VisualReferencePath; the
	// `imagediff` tool decides PASS/FAIL deterministically at the recipe threshold.
	// The render is OPTIONAL/tooling-dependent (a documented npm script), but the
	// DIFF gate is the deterministic decision and runs regardless: a missing
	// rendered file is a clear imagediff error (deterministic FAIL), never a skip.
	StackWeb: {
		Stack:  StackWeb,
		Build:  []string{"npm", "run", "build"},
		Test:   []string{"npm", "test"},
		Vet:    []string{"npm", "run", "typecheck"},
		Lint:   []string{"npx", "eslint", "."},
		Visual: []string{"imagediff", VisualActualPath, VisualReferencePath, "-threshold", DefaultVisualThreshold},
	},
	// StackIOS is the maestro-driven mobile/iOS recipe (2A-3). It mirrors StackWeb
	// (ADR-0023) on a Mac host:
	//   - Build/Test: `xcodebuild test` — compiles + runs the iOS test suite. This
	//     REQUIRES Xcode/an iOS toolchain, so it runs LIVE only on a Mac host
	//     (Dalga B); it is recorded here as the deterministic build/test leg.
	//   - Maestro: `maestro test <flow>` — a UI-flow gate whose exit code is the
	//     verdict (a command gate like any other). It drives a simulator on the Mac
	//     host (LIVE, Dalga B) and can capture the screenshot.
	//   - Visual: the SAME deterministic imagediff gate the web recipe uses — the
	//     produced screenshot vs the holdout-injected reference at the threshold.
	// The xcodebuild/maestro binaries are operator-provided; if absent the gate
	// FAILS deterministically (missing-binary, fix-#1 pattern) — never a silent
	// skip. The visual DECISION (imagediff) is offline-verifiable here; the live
	// iOS-build + maestro-on-simulator run is deferred to Dalga B (Mac host),
	// routed there via Capability = "ios-build" (ADR-0008; routing is 2B-2).
	StackIOS: {
		Stack:      StackIOS,
		Build:      []string{"xcodebuild", "build-for-testing"},
		Test:       []string{"xcodebuild", "test"},
		Maestro:    []string{"maestro", "test", MaestroFlowPath},
		Visual:     []string{"imagediff", VisualActualPath, VisualReferencePath, "-threshold", DefaultVisualThreshold},
		Capability: CapabilityIOSBuild,
	},
	StackPython: {
		Stack: StackPython,
		Build: []string{"python", "-m", "build"},
		Test:  []string{"pytest"},
		Vet:   []string{"mypy", "."},
		Lint:  []string{"ruff", "check", "."},
	},
	StackRust: {
		Stack: StackRust,
		Build: []string{"cargo", "build"},
		Test:  []string{"cargo", "test"},
		Vet:   []string{"cargo", "check"},
		Lint:  []string{"cargo", "clippy"},
	},
}

// ProfileFor returns the recipe profile for stack and whether one exists. An
// unknown stack has no profile (ok=false): the caller must not invent gate
// commands for a repo it could not classify.
func ProfileFor(stack Stack) (Profile, bool) {
	p, ok := profiles[stack]
	return p, ok
}

// Readiness is the verdict of the readiness-gate (ADR-0009): whether the repo has
// a deterministic test gate the platform can rely on. Reason is always populated
// — an actionable explanation when not Ready, a confirmation when Ready.
type Readiness struct {
	// Ready is true only when test infrastructure for the stack was found.
	Ready bool
	// Reason is a human-actionable explanation of the verdict.
	Reason string
}

// readinessProbes maps each stack to the predicate that decides whether the repo
// has test infrastructure. Keeping it data-driven keeps AssessReadiness's control
// flow stack-agnostic.
var readinessProbes = map[Stack]func(dir string) bool{
	StackGo:     hasGoTests,
	StackNode:   hasNodeTests,
	StackPython: hasPythonTests,
	StackRust:   hasRustTests,
	// A web project is a Node project at heart: the same test-infrastructure
	// probe applies (the visual gate is additive, not a substitute for tests).
	StackWeb: hasNodeTests,
	// An iOS project's deterministic gate is its maestro UI-flow and/or an Xcode
	// test target; hasIOSTests looks for either (the visual gate is additive).
	StackIOS: hasIOSTests,
}

// readinessHints names, per stack, the test signal we look for, so the
// NOT-READY reason can tell a human exactly what to add.
var readinessHints = map[Stack]string{
	StackGo:     "a *_test.go file",
	StackNode:   `a "test" script in package.json or a test/__tests__ directory`,
	StackPython: "a tests/ directory or test_*.py / *_test.py file",
	StackRust:   "#[test] functions or a tests/ directory",
	StackWeb:    `a "test" script in package.json or a test/__tests__ directory`,
	StackIOS:    "a maestro/ flows directory (*.yaml/*.yml) or an Xcode *Tests target/dir",
}

// AssessReadiness applies the readiness-gate for the given stack against dir
// (ADR-0009 §Readiness-gate). It returns READY only when stack-appropriate test
// infrastructure is found; otherwise it returns NOT-READY with an actionable
// reason naming the missing signal, so the platform's first task becomes "set up
// quality infrastructure" rather than running a feature task against a repo with
// no deterministic gate.
//
// An unknown stack is NOT-READY: a repo we could not classify has, by
// definition, no gate we can trust.
func AssessReadiness(dir string, stack Stack) Readiness {
	probe, ok := readinessProbes[stack]
	if !ok {
		return Readiness{
			Ready:  false,
			Reason: fmt.Sprintf("unknown stack %q: cannot establish a deterministic test gate — set up quality infrastructure first per ADR-0009", stack),
		}
	}
	if probe(dir) {
		return Readiness{
			Ready:  true,
			Reason: fmt.Sprintf("%s test infrastructure detected — deterministic gate available", stack),
		}
	}
	return Readiness{
		Ready: false,
		Reason: fmt.Sprintf(
			"no tests detected for %s stack (looked for %s) — set up quality infrastructure first per ADR-0009",
			stack, readinessHints[stack],
		),
	}
}

// hasGoTests reports whether dir's tree contains any Go test file (*_test.go),
// the deterministic gate anchor for the Go stack.
func hasGoTests(dir string) bool {
	return walkAnyFile(dir, func(name string) bool {
		return strings.HasSuffix(name, "_test.go")
	})
}

// hasNodeTests reports whether the Node repo has a usable test gate: a non-empty
// "test" script in package.json, or a conventional test directory.
func hasNodeTests(dir string) bool {
	if packageJSONHasTestScript(filepath.Join(dir, "package.json")) {
		return true
	}
	for _, d := range []string{"test", "tests", "__tests__"} {
		if info, err := os.Stat(filepath.Join(dir, d)); err == nil && info.IsDir() {
			return true
		}
	}
	// Spec-style files anywhere in the tree also count.
	return walkAnyFile(dir, func(name string) bool {
		return strings.HasSuffix(name, ".test.js") || strings.HasSuffix(name, ".test.ts") ||
			strings.HasSuffix(name, ".spec.js") || strings.HasSuffix(name, ".spec.ts")
	})
}

// hasPythonTests reports whether the Python repo has a tests directory or any
// test_*.py / *_test.py file in its tree.
func hasPythonTests(dir string) bool {
	for _, d := range []string{"tests", "test"} {
		if info, err := os.Stat(filepath.Join(dir, d)); err == nil && info.IsDir() {
			return true
		}
	}
	return walkAnyFile(dir, func(name string) bool {
		return (strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py")) &&
			strings.HasSuffix(name, ".py")
	})
}

// hasRustTests reports whether the Rust repo has a tests/ integration directory
// or any #[test] attribute in a .rs source file.
func hasRustTests(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "tests")); err == nil && info.IsDir() {
		return true
	}
	return walkAnyFileContent(dir, ".rs", func(content []byte) bool {
		return strings.Contains(string(content), "#[test]")
	})
}

// hasIOSTests reports whether the iOS repo has a deterministic UI/test gate: a
// `maestro/` flows directory containing at least one flow file (*.yaml/*.yml), or
// an Xcode test target (a name containing "Tests" — a *Tests dir or *Tests.swift
// file). Either is enough for the maestro/visual recipe; the visual gate is
// additive and not a substitute for it.
func hasIOSTests(dir string) bool {
	flows := filepath.Join(dir, "maestro")
	if info, err := os.Stat(flows); err == nil && info.IsDir() {
		if walkAnyFile(flows, func(name string) bool {
			return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
		}) {
			return true
		}
	}
	// An Xcode test target: a directory or Swift file whose name carries "Tests".
	return walkAnyFile(dir, func(name string) bool {
		return strings.HasSuffix(name, "Tests.swift") || strings.HasSuffix(name, "Tests.m")
	}) || walkAnyDir(dir, func(name string) bool {
		return strings.HasSuffix(name, "Tests")
	})
}

// packageJSONHasTestScript reports whether path is a package.json declaring a
// non-empty, non-placeholder "test" script. The classic `npm init` placeholder
// (which exits 1) does NOT count as a real gate.
func packageJSONHasTestScript(path string) bool {
	data, err := os.ReadFile(path) //nolint:gosec // path is the operator-supplied repo dir under onboarding.
	if err != nil {
		return false
	}
	var doc struct {
		Scripts map[string]string `yaml:"scripts" json:"scripts"`
	}
	// package.json is JSON, which yaml.v3 parses as a superset; this avoids a
	// second JSON dependency while reusing the lib already in go.mod.
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false
	}
	test, ok := doc.Scripts["test"]
	if !ok {
		return false
	}
	test = strings.TrimSpace(test)
	if test == "" {
		return false
	}
	return !strings.Contains(test, "no test specified")
}

// walkAnyFile walks dir's tree and returns true at the first regular file whose
// base name satisfies match. It skips VCS/dependency dirs (.git, node_modules,
// vendor, target) so detection is fast and not fooled by third-party tests.
func walkAnyFile(dir string, match func(base string) bool) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name(), path, dir) {
				return filepath.SkipDir
			}
			return nil
		}
		if match(d.Name()) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// walkAnyDir walks dir's tree and returns true at the first DIRECTORY whose base
// name satisfies match. It skips VCS/dependency dirs like walkAnyFile so detection
// is fast and not fooled by third-party directories. The repo root itself is not
// matched (a walk starts at it but it is skipped via shouldSkipDir's root guard
// only for pruning; the root's own name is still passed, so callers should use
// suffix matches specific enough not to match the root).
func walkAnyDir(dir string, match func(base string) bool) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if shouldSkipDir(d.Name(), path, dir) {
			return filepath.SkipDir
		}
		if path != dir && match(d.Name()) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// walkAnyFileContent walks dir's tree for regular files with the given extension
// and returns true at the first whose content satisfies match. It applies the
// same dir-skip rules as walkAnyFile and ignores files it cannot read.
func walkAnyFileContent(dir, ext string, match func(content []byte) bool) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name(), path, dir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ext) {
			return nil
		}
		content, rerr := os.ReadFile(path) //nolint:gosec // path is under the operator-supplied repo dir.
		if rerr != nil {
			return nil
		}
		if match(content) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// shouldSkipDir reports whether a directory should be pruned from readiness
// walks. The repo root itself is never skipped; nested VCS/dependency/build dirs
// are.
func shouldSkipDir(name, path, root string) bool {
	if path == root {
		return false
	}
	switch name {
	case ".git", "node_modules", "vendor", "target", ".venv", "venv", "dist", "build", ".conductor":
		return true
	default:
		return false
	}
}

// Draft is the deterministic onboarding result for a repo (ADR-0009): the
// detection, selected profile, readiness verdict, and the rendered `.conductor/`
// config. It is the "assisted draft" handed to a human for approval; nothing here
// is written to disk until WriteDraft is called.
type Draft struct {
	// Detection is the stack detection result.
	Detection Detection
	// Profile is the selected per-stack recipe profile (zero value if unknown).
	Profile Profile
	// HasProfile reports whether a profile was found for the primary stack.
	HasProfile bool
	// Readiness is the readiness-gate verdict.
	Readiness Readiness
	// BaseBranch is the default integration branch written into the config.
	BaseBranch string
	// Config is the rendered .conductor/config.yaml document.
	Config []byte
}

// DefaultBaseBranch is the integration branch a drafted config defaults to when
// the caller does not specify one. It mirrors conductorctl's default so an
// onboarded project and the control plane agree.
const DefaultBaseBranch = "develop"

// recipeDoc is the on-disk shape of `.conductor/config.yaml`. Field names mirror
// the recipe contract (ADR-0002/0009): a develop command, the verify gate
// commands, the base branch, and the registry pointer. argv slices match
// engine.RecipeConfig and verify.Gate so the platform consumes them directly.
type recipeDoc struct {
	// Version is the config schema version for forward compatibility.
	Version int `yaml:"version"`
	// Stack is the detected primary stack, recorded for transparency.
	Stack string `yaml:"stack"`
	// BaseBranch is the integration branch tasks branch from (ADR-0004).
	BaseBranch string `yaml:"base_branch"`
	// Readiness is the onboarding readiness verdict (ready / not-ready).
	Readiness recipeReadiness `yaml:"readiness"`
	// Requires is the OPTIONAL host capability this recipe's live gate run needs
	// (ADR-0008 lane `requires`): e.g. "ios-build" for the iOS recipe, so Dalga-B
	// capability routing (2B-2) sends its live build/maestro run to a Mac host. It
	// is the routing HOOK recorded on the recipe; the routing itself is Dalga B.
	// Omitted for stacks runnable on any host.
	Requires string `yaml:"requires,omitempty"`
	// Recipe holds the develop command and verify gate commands.
	Recipe recipeCommands `yaml:"recipe"`
}

// recipeReadiness is the persisted readiness-gate verdict.
type recipeReadiness struct {
	// Ready is the readiness boolean.
	Ready bool `yaml:"ready"`
	// Reason is the actionable explanation.
	Reason string `yaml:"reason"`
}

// recipeCommands is the recipe body: the develop command and the ordered verify
// gate commands, each an argv slice.
type recipeCommands struct {
	// Develop is the argv the CommandEngine drives for development (ADR-0002).
	Develop []string `yaml:"develop"`
	// Verify lists the deterministic gate commands the verify step runs.
	Verify recipeGates `yaml:"verify"`
}

// recipeGates names the four standard gate slots; empty slots are omitted from
// the rendered YAML so an unknown-stack draft stays honest.
type recipeGates struct {
	Build []string `yaml:"build,omitempty"`
	Test  []string `yaml:"test,omitempty"`
	Vet   []string `yaml:"vet,omitempty"`
	Lint  []string `yaml:"lint,omitempty"`
	// Maestro is the optional maestro UI-flow gate (2A-3): the argv that runs a
	// maestro test flow whose exit code is the verdict. Omitted for non-iOS stacks.
	Maestro []string `yaml:"maestro,omitempty"`
	// Visual is the optional deterministic visual-diff gate (ADR-0023): the argv
	// that runs the imagediff tool against the rendered screenshot and the
	// holdout-injected reference. Omitted for non-visual stacks (set for web + iOS).
	Visual []string `yaml:"visual,omitempty"`
}

// GenerateDraft runs the full deterministic onboarding pipeline against dir:
// detect → select profile → assess readiness → render `.conductor/config.yaml`.
// baseBranch defaults to DefaultBaseBranch when empty. It never writes to disk;
// callers persist the result with WriteDraft after (human) approval.
func GenerateDraft(dir, baseBranch string) (Draft, error) {
	if baseBranch == "" {
		baseBranch = DefaultBaseBranch
	}
	det := DetectStack(dir)
	profile, hasProfile := ProfileFor(det.Primary)
	readiness := AssessReadiness(dir, det.Primary)

	doc := recipeDoc{
		Version:    1,
		Stack:      det.Primary.String(),
		BaseBranch: baseBranch,
		Readiness:  recipeReadiness(readiness),
	}
	if hasProfile {
		// Record the lane's host-capability requirement (ADR-0008 routing hook):
		// e.g. iOS's "ios-build" so Dalga-B routing (2B-2) sends its live run to a
		// Mac host. Empty for stacks runnable on any host (omitted from the YAML).
		doc.Requires = profile.Capability
		doc.Recipe = recipeCommands{
			// The develop command is the performer entrypoint; the profile does not
			// prescribe it (it is engine/host wiring), so onboarding records the
			// platform default placeholder for a human to confirm (ADR-0009).
			Develop: developPlaceholder(),
			Verify: recipeGates{
				Build:   profile.Build,
				Test:    profile.Test,
				Vet:     profile.Vet,
				Lint:    profile.Lint,
				Maestro: profile.Maestro,
				Visual:  profile.Visual,
			},
		}
	}

	rendered, err := renderConfig(doc)
	if err != nil {
		return Draft{}, fmt.Errorf("scaffolder: render config: %w", err)
	}

	return Draft{
		Detection:  det,
		Profile:    profile,
		HasProfile: hasProfile,
		Readiness:  readiness,
		BaseBranch: baseBranch,
		Config:     rendered,
	}, nil
}

// developPlaceholder is the develop command onboarding writes for a human to
// confirm. It is intentionally inert (a no-op echo) so an un-reviewed draft can
// never silently launch a real performer.
func developPlaceholder() []string {
	return []string{"echo", "configure-develop-command"}
}

// renderConfig serializes a recipeDoc to YAML with a header comment that states
// the draft is generated and requires human approval (ADR-0009 assisted-draft).
func renderConfig(doc recipeDoc) ([]byte, error) {
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, err
	}
	header := "# .conductor/config.yaml — generated by the scaffolder (ADR-0009).\n" +
		"# This is an ASSISTED DRAFT: review and approve before the platform runs tasks.\n"
	return append([]byte(header), body...), nil
}

// WriteDraft writes the draft's rendered config to <targetDir>/.conductor/config.yaml,
// creating the .conductor directory if needed. It refuses to overwrite an
// existing config (onboarding must not clobber a repo's approved recipe) and
// returns the path written. A draft with no rendered config is a programmer
// error and is rejected.
func WriteDraft(targetDir string, draft Draft) (string, error) {
	if len(draft.Config) == 0 {
		return "", fmt.Errorf("scaffolder: write draft: empty config (call GenerateDraft first)")
	}
	confDir := filepath.Join(targetDir, ".conductor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return "", fmt.Errorf("scaffolder: create .conductor dir: %w", err)
	}
	confPath := filepath.Join(confDir, "config.yaml")
	if _, err := os.Stat(confPath); err == nil {
		return "", fmt.Errorf("scaffolder: %s already exists; refusing to overwrite", confPath)
	}
	if err := os.WriteFile(confPath, draft.Config, 0o644); err != nil { //nolint:gosec // config is non-secret onboarding metadata.
		return "", fmt.Errorf("scaffolder: write config: %w", err)
	}
	return confPath, nil
}

// GateSpec is one resolved verify gate read back from a `.conductor/config.yaml`:
// a human Name (the slot it came from: build/test/vet/lint) and its argv (program
// + args, executed without a shell). It is the read-side mirror of the recipeGates
// the scaffolder EMITS, so the daemon consumes the SAME config the scaffolder
// drafts (closing the N-8 scaffolder→daemon recipe gap). It is deliberately a
// plain, dependency-free struct so cmd/conductor can map it onto verify.Gate
// without this package importing internal/verify.
type GateSpec struct {
	// Name is the gate slot ("build"/"test"/"vet"/"lint"), surfaced in the Check.
	Name string
	// Argv is the command to run (program + args), no shell.
	Argv []string
}

// LoadRecipeGates reads <repoDir>/.conductor/config.yaml and returns its verify
// gates in deterministic order (build, test, vet, lint), skipping empty slots. It
// is the read-side counterpart of GenerateDraft's emitter and reuses the EXACT
// on-disk shape (recipeDoc) so the daemon honors whatever the scaffolder drafted
// (incl. an opt-in lint gate).
//
// The bool reports whether a config file was present: false (with a nil error)
// means the repo has no `.conductor/config.yaml`, so the caller falls back to its
// built-in default gates (backward compatible — a config-less repo is unchanged).
// A present-but-unparseable or gate-less config is an error: a corrupt recipe must
// not silently degrade the merge gate (that would be a fake-green).
func LoadRecipeGates(repoDir string) ([]GateSpec, bool, error) {
	rec, err := LoadRecipe(repoDir)
	if err != nil {
		if errors.Is(err, ErrNoRecipe) {
			// No `.conductor/config.yaml` — the caller falls back to its built-in
			// default gates. Preserve the original (false, nil) contract.
			return nil, false, nil
		}
		return nil, false, err
	}
	return rec.Gates, true, nil
}

// ErrNoRecipe is the sentinel LoadRecipe returns when a repo has NO
// `.conductor/config.yaml`. It is the "no recipe declared" signal that lets a
// caller (the daemon) fall back to its built-in develop command and default
// gates — a config-less repo is unchanged (backward compatible). Detect it with
// errors.Is; any OTHER error from LoadRecipe (unreadable / corrupt / gate-less)
// is a hard failure that must NOT silently degrade the merge gate.
var ErrNoRecipe = errors.New("scaffolder: no .conductor/config.yaml recipe")

// Recipe is the full per-project recipe read back from a `.conductor/config.yaml`
// (ADR-0009): the develop command the CommandEngine drives (ADR-0002) AND the
// ordered verify gate commands the verify step runs (ADR-0003). It is the
// read-side mirror of the recipeDoc GenerateDraft EMITS, so the daemon consumes
// the SAME recipe the scaffolder drafts — closing the per-project recipe gap
// (2A-1): a project declares BOTH its performer and its gates in one file.
//
// Develop may be empty when a draft was generated for a repo with no profile
// (an unknown stack), or it may still be the inert onboarding placeholder
// (developPlaceholder) if a human has not yet replaced it. Callers decide
// whether an empty/placeholder develop is acceptable; LoadRecipe reports the
// file faithfully and does not invent a command.
type Recipe struct {
	// Develop is the develop command argv (program + args), no shell. It is the
	// per-project performer entrypoint the CommandEngine runs (engine.RecipeConfig.DevelopCmd).
	Develop []string
	// Gates are the verify gate commands in deterministic order (build, test, vet,
	// lint, maestro, visual), empty slots skipped. Same shape LoadRecipeGates returns.
	Gates []GateSpec
	// Requires is the OPTIONAL host capability this recipe's live gate run needs
	// (ADR-0008 lane `requires`): e.g. "ios-build" for iOS. Empty for stacks
	// runnable on any host. It is the Dalga-B (2B-2) capability-routing hook read
	// back from the recipe; routing itself is Dalga B.
	Requires string
}

// LoadRecipe reads <repoDir>/.conductor/config.yaml and returns the full recipe:
// the develop command argv plus the ordered verify gates (build, test, vet,
// lint; empty slots skipped). It is the per-project counterpart of GenerateDraft's
// emitter and reuses the EXACT on-disk shape (recipeDoc) so the daemon honors
// whatever the scaffolder drafted (develop command + gates, incl. an opt-in lint
// gate).
//
// A MISSING file returns ErrNoRecipe (detect with errors.Is) so the caller falls
// back to its built-in defaults — a config-less repo is unchanged. A present-but-
// unreadable or unparseable config, or one declaring no verify gates, is a hard
// error: a corrupt recipe must not silently degrade the merge gate (a fake-green).
func LoadRecipe(repoDir string) (Recipe, error) {
	path := filepath.Join(repoDir, ".conductor", "config.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // path is the operator-supplied repo dir's recipe config.
	if err != nil {
		if os.IsNotExist(err) {
			return Recipe{}, ErrNoRecipe
		}
		return Recipe{}, fmt.Errorf("scaffolder: read recipe config %s: %w", path, err)
	}

	var doc recipeDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Recipe{}, fmt.Errorf("scaffolder: parse recipe config %s: %w", path, err)
	}

	g := doc.Recipe.Verify
	slots := []struct {
		name string
		argv []string
	}{
		{"build", g.Build},
		{"test", g.Test},
		{"vet", g.Vet},
		{"lint", g.Lint},
		{"maestro", g.Maestro},
		{"visual", g.Visual},
	}
	gates := make([]GateSpec, 0, len(slots))
	for _, s := range slots {
		if len(s.argv) > 0 {
			gates = append(gates, GateSpec{Name: s.name, Argv: s.argv})
		}
	}
	if len(gates) == 0 {
		return Recipe{}, fmt.Errorf("scaffolder: recipe config %s declares no verify gates", path)
	}
	return Recipe{Develop: doc.Recipe.Develop, Gates: gates, Requires: doc.Requires}, nil
}

// SupportedStacks returns the stacks the scaffolder can onboard, in a stable
// sorted order, for help text and tests. StackUnknown is excluded — it is a
// verdict, not a supported stack.
func SupportedStacks() []Stack {
	out := make([]Stack, 0, len(profiles))
	for s := range profiles {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
