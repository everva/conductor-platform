package scaffolder

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// fixture returns the absolute path to a testdata fixture repo.
func fixture(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("testdata", name)
}

func TestDetectStack(t *testing.T) {
	tests := []struct {
		name        string
		fixture     string
		wantPrimary Stack
		wantAll     []Stack
	}{
		{"go", "go-with-tests", StackGo, []Stack{StackGo}},
		{"node", "node-repo", StackNode, []Stack{StackNode}},
		{"python", "python-repo", StackPython, []Stack{StackPython}},
		{"rust", "rust-repo", StackRust, []Stack{StackRust}},
		// A Node repo carrying a playwright.config.* is the "web" stack; Node is
		// still reported in All (it has a package.json) but Web wins precedence.
		{"web", "web-repo", StackWeb, []Stack{StackWeb, StackNode}},
		{"unknown", "empty-unknown", StackUnknown, nil},
		// Multi-stack: Go marker wins precedence over Node; both are reported.
		{"multistack-go-primary", "multistack", StackGo, []Stack{StackGo, StackNode}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			det := DetectStack(fixture(t, tt.fixture))
			if det.Primary != tt.wantPrimary {
				t.Errorf("Primary = %q, want %q", det.Primary, tt.wantPrimary)
			}
			if !reflect.DeepEqual(det.All, tt.wantAll) {
				t.Errorf("All = %v, want %v", det.All, tt.wantAll)
			}
		})
	}
}

func TestDetectStackMissingDir(t *testing.T) {
	det := DetectStack(filepath.Join("testdata", "does-not-exist"))
	if det.Primary != StackUnknown {
		t.Errorf("missing dir Primary = %q, want unknown", det.Primary)
	}
}

func TestProfileFor(t *testing.T) {
	tests := []struct {
		stack     Stack
		wantOK    bool
		wantBuild []string
		wantTest  []string
		wantVet   []string
		wantLint  []string
	}{
		{StackGo, true,
			[]string{"go", "build", "./..."},
			[]string{"go", "test", "./..."},
			[]string{"go", "vet", "./..."},
			[]string{"golangci-lint", "run"}},
		{StackNode, true,
			[]string{"npm", "run", "build"},
			[]string{"npm", "test"},
			[]string{"npm", "run", "typecheck"},
			[]string{"npx", "eslint", "."}},
		{StackPython, true,
			[]string{"python", "-m", "build"},
			[]string{"pytest"},
			[]string{"mypy", "."},
			[]string{"ruff", "check", "."}},
		{StackRust, true,
			[]string{"cargo", "build"},
			[]string{"cargo", "test"},
			[]string{"cargo", "check"},
			[]string{"cargo", "clippy"}},
		{StackUnknown, false, nil, nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.stack.String(), func(t *testing.T) {
			p, ok := ProfileFor(tt.stack)
			if ok != tt.wantOK {
				t.Fatalf("ProfileFor ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(p.Build, tt.wantBuild) {
				t.Errorf("Build = %v, want %v", p.Build, tt.wantBuild)
			}
			if !reflect.DeepEqual(p.Test, tt.wantTest) {
				t.Errorf("Test = %v, want %v", p.Test, tt.wantTest)
			}
			if !reflect.DeepEqual(p.Vet, tt.wantVet) {
				t.Errorf("Vet = %v, want %v", p.Vet, tt.wantVet)
			}
			if !reflect.DeepEqual(p.Lint, tt.wantLint) {
				t.Errorf("Lint = %v, want %v", p.Lint, tt.wantLint)
			}
		})
	}
}

func TestProfileGates(t *testing.T) {
	p, _ := ProfileFor(StackGo)
	gates := p.Gates()
	want := [][]string{
		{"go", "build", "./..."},
		{"go", "test", "./..."},
		{"go", "vet", "./..."},
		{"golangci-lint", "run"},
	}
	if !reflect.DeepEqual(gates, want) {
		t.Errorf("Gates() = %v, want %v", gates, want)
	}
}

func TestAssessReadiness(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		stack     Stack
		wantReady bool
		reasonHas string
	}{
		{"go-with-tests", "go-with-tests", StackGo, true, "deterministic gate available"},
		{"go-no-tests", "go-no-tests", StackGo, false, "no tests detected"},
		{"node-with-test-script", "node-repo", StackNode, true, "deterministic gate available"},
		{"node-placeholder-test", "node-no-tests", StackNode, false, "no tests detected"},
		{"python-with-tests-dir", "python-repo", StackPython, true, "deterministic gate available"},
		{"python-no-tests", "python-no-tests", StackPython, false, "no tests detected"},
		{"rust-with-test-attr", "rust-repo", StackRust, true, "deterministic gate available"},
		{"unknown-stack", "empty-unknown", StackUnknown, false, "unknown stack"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := AssessReadiness(fixture(t, tt.fixture), tt.stack)
			if r.Ready != tt.wantReady {
				t.Errorf("Ready = %v, want %v (reason: %s)", r.Ready, tt.wantReady, r.Reason)
			}
			if !strings.Contains(r.Reason, tt.reasonHas) {
				t.Errorf("Reason = %q, want substring %q", r.Reason, tt.reasonHas)
			}
			// Not-ready reasons must point at the ADR-0009 remedy.
			if !tt.wantReady && !strings.Contains(r.Reason, "ADR-0009") {
				t.Errorf("not-ready reason must reference ADR-0009, got %q", r.Reason)
			}
		})
	}
}

func TestGenerateDraftGoReady(t *testing.T) {
	draft, err := GenerateDraft(fixture(t, "go-with-tests"), "")
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.Detection.Primary != StackGo {
		t.Errorf("Primary = %q, want go", draft.Detection.Primary)
	}
	if !draft.HasProfile {
		t.Fatal("expected a profile for go")
	}
	if !draft.Readiness.Ready {
		t.Errorf("expected ready, got reason %q", draft.Readiness.Reason)
	}
	if draft.BaseBranch != DefaultBaseBranch {
		t.Errorf("BaseBranch = %q, want %q", draft.BaseBranch, DefaultBaseBranch)
	}

	// Parse the rendered config back and assert its exact shape.
	var doc recipeDoc
	if err := yaml.Unmarshal(draft.Config, &doc); err != nil {
		t.Fatalf("rendered config is not valid YAML: %v", err)
	}
	if doc.Version != 1 {
		t.Errorf("version = %d, want 1", doc.Version)
	}
	if doc.Stack != "go" {
		t.Errorf("stack = %q, want go", doc.Stack)
	}
	if doc.BaseBranch != "develop" {
		t.Errorf("base_branch = %q, want develop", doc.BaseBranch)
	}
	if !doc.Readiness.Ready {
		t.Error("rendered readiness.ready = false, want true")
	}
	wantVerify := recipeGates{
		Build: []string{"go", "build", "./..."},
		Test:  []string{"go", "test", "./..."},
		Vet:   []string{"go", "vet", "./..."},
		Lint:  []string{"golangci-lint", "run"},
	}
	if !reflect.DeepEqual(doc.Recipe.Verify, wantVerify) {
		t.Errorf("verify gates = %+v, want %+v", doc.Recipe.Verify, wantVerify)
	}
	if len(doc.Recipe.Develop) == 0 {
		t.Error("expected a develop placeholder command")
	}
	// Header comment must flag the assisted-draft / approval requirement.
	if !strings.Contains(string(draft.Config), "ASSISTED DRAFT") {
		t.Error("rendered config missing assisted-draft header")
	}
}

func TestGenerateDraftUnknownStackNotReady(t *testing.T) {
	draft, err := GenerateDraft(fixture(t, "empty-unknown"), "main")
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.Detection.Primary != StackUnknown {
		t.Errorf("Primary = %q, want unknown", draft.Detection.Primary)
	}
	if draft.HasProfile {
		t.Error("unknown stack must not yield a profile")
	}
	if draft.Readiness.Ready {
		t.Error("unknown stack must be not-ready")
	}
	if draft.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want main", draft.BaseBranch)
	}
	// Unknown stack: verify gates omitted from the YAML.
	var doc recipeDoc
	if err := yaml.Unmarshal(draft.Config, &doc); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	if len(doc.Recipe.Verify.Build) != 0 || len(doc.Recipe.Verify.Test) != 0 {
		t.Errorf("unknown stack should have empty verify gates, got %+v", doc.Recipe.Verify)
	}
}

func TestWriteDraft(t *testing.T) {
	draft, err := GenerateDraft(fixture(t, "node-repo"), "")
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	target := t.TempDir()
	path, err := WriteDraft(target, draft)
	if err != nil {
		t.Fatalf("WriteDraft: %v", err)
	}
	want := filepath.Join(target, ".conductor", "config.yaml")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path) //nolint:gosec // test-controlled temp path.
	if err != nil {
		t.Fatalf("read written config: %v", err)
	}
	if !reflect.DeepEqual(data, draft.Config) {
		t.Error("written bytes differ from draft.Config")
	}

	// Re-writing must refuse to clobber an approved recipe.
	if _, err := WriteDraft(target, draft); err == nil {
		t.Error("expected WriteDraft to refuse overwrite of existing config")
	}
}

func TestWriteDraftEmptyConfig(t *testing.T) {
	if _, err := WriteDraft(t.TempDir(), Draft{}); err == nil {
		t.Error("expected error writing an empty draft")
	}
}

func TestSupportedStacks(t *testing.T) {
	got := SupportedStacks()
	want := []Stack{StackGo, StackNode, StackPython, StackRust, StackWeb}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SupportedStacks() = %v, want %v", got, want)
	}
}

// TestLoadRecipeGates_RoundTrip proves the read-side LoadRecipeGates consumes the
// EXACT config GenerateDraft emits: a drafted Go recipe round-trips back to its
// four ordered gates (build, test, vet, lint), closing the scaffolder→daemon gap.
func TestLoadRecipeGates_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	draft, err := GenerateDraft(dir, "develop")
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	// Force a Go profile draft regardless of the empty temp dir's detection by
	// writing the rendered config directly under .conductor.
	goDraft, err := GenerateDraft(goRepoDir(t), "develop")
	if err != nil {
		t.Fatalf("GenerateDraft(go): %v", err)
	}
	_ = draft
	confDir := filepath.Join(dir, ".conductor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "config.yaml"), goDraft.Config, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	gates, found, err := LoadRecipeGates(dir)
	if err != nil {
		t.Fatalf("LoadRecipeGates: %v", err)
	}
	if !found {
		t.Fatal("found = false, want true (config present)")
	}
	want := []GateSpec{
		{Name: "build", Argv: []string{"go", "build", "./..."}},
		{Name: "test", Argv: []string{"go", "test", "./..."}},
		{Name: "vet", Argv: []string{"go", "vet", "./..."}},
		{Name: "lint", Argv: []string{"golangci-lint", "run"}},
	}
	if len(gates) != len(want) {
		t.Fatalf("got %d gates, want %d: %+v", len(gates), len(want), gates)
	}
	for i := range want {
		if gates[i].Name != want[i].Name || strings.Join(gates[i].Argv, " ") != strings.Join(want[i].Argv, " ") {
			t.Fatalf("gate[%d] = %+v, want %+v", i, gates[i], want[i])
		}
	}
}

// TestLoadRecipeGates_NoConfig proves a repo with no .conductor/config.yaml
// reports found=false with a nil error, so the daemon falls back to its defaults.
func TestLoadRecipeGates_NoConfig(t *testing.T) {
	_, found, err := LoadRecipeGates(t.TempDir())
	if err != nil {
		t.Fatalf("LoadRecipeGates(no config): unexpected error %v", err)
	}
	if found {
		t.Fatal("found = true, want false for a repo with no .conductor/config.yaml")
	}
}

// TestLoadRecipe_RoundTrip proves the full per-project recipe (2A-1) round-trips:
// GenerateDraft → LoadRecipe yields BOTH the develop command and the ordered gates
// for go/node/python, reusing the EXACT on-disk shape. The develop command of a
// fresh draft is the inert onboarding placeholder (un-reviewed), which LoadRecipe
// reports faithfully — the daemon decides whether to honor it.
func TestLoadRecipe_RoundTrip(t *testing.T) {
	cases := []struct {
		name      string
		repoDir   func(t *testing.T) string
		wantGates []GateSpec
	}{
		{
			name:    "go",
			repoDir: goRepoDir,
			wantGates: []GateSpec{
				{Name: "build", Argv: []string{"go", "build", "./..."}},
				{Name: "test", Argv: []string{"go", "test", "./..."}},
				{Name: "vet", Argv: []string{"go", "vet", "./..."}},
				{Name: "lint", Argv: []string{"golangci-lint", "run"}},
			},
		},
		{
			name:    "node",
			repoDir: func(t *testing.T) string { return fixture(t, "node-repo") },
			wantGates: []GateSpec{
				{Name: "build", Argv: []string{"npm", "run", "build"}},
				{Name: "test", Argv: []string{"npm", "test"}},
				{Name: "vet", Argv: []string{"npm", "run", "typecheck"}},
				{Name: "lint", Argv: []string{"npx", "eslint", "."}},
			},
		},
		{
			name:    "python",
			repoDir: func(t *testing.T) string { return fixture(t, "python-repo") },
			wantGates: []GateSpec{
				{Name: "build", Argv: []string{"python", "-m", "build"}},
				{Name: "test", Argv: []string{"pytest"}},
				{Name: "vet", Argv: []string{"mypy", "."}},
				{Name: "lint", Argv: []string{"ruff", "check", "."}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft, err := GenerateDraft(tc.repoDir(t), "develop")
			if err != nil {
				t.Fatalf("GenerateDraft: %v", err)
			}
			dir := t.TempDir()
			confDir := filepath.Join(dir, ".conductor")
			if err := os.MkdirAll(confDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(confDir, "config.yaml"), draft.Config, 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			rec, err := LoadRecipe(dir)
			if err != nil {
				t.Fatalf("LoadRecipe: %v", err)
			}
			// A fresh draft carries the inert placeholder develop command.
			if !reflect.DeepEqual(rec.Develop, developPlaceholder()) {
				t.Fatalf("develop = %v, want placeholder %v", rec.Develop, developPlaceholder())
			}
			if !reflect.DeepEqual(rec.Gates, tc.wantGates) {
				t.Fatalf("gates = %+v, want %+v", rec.Gates, tc.wantGates)
			}
		})
	}
}

// TestWebProfile proves the web stack profile (ADR-0023): it carries the standard
// Node gates PLUS the deterministic visual-diff gate calling the imagediff tool
// against the rendered screenshot and the holdout-injected reference, last in the
// ordered gate list.
func TestWebProfile(t *testing.T) {
	p, ok := ProfileFor(StackWeb)
	if !ok {
		t.Fatal("ProfileFor(web) not found")
	}
	wantVisual := []string{"imagediff", VisualActualPath, VisualReferencePath, "-threshold", DefaultVisualThreshold}
	if !reflect.DeepEqual(p.Visual, wantVisual) {
		t.Fatalf("web Visual gate = %v, want %v", p.Visual, wantVisual)
	}
	gates := p.Gates()
	if len(gates) != 5 {
		t.Fatalf("web profile gates = %d, want 5 (build,test,vet,lint,visual)", len(gates))
	}
	if !reflect.DeepEqual(gates[len(gates)-1], wantVisual) {
		t.Fatalf("visual gate must be LAST, got %v", gates[len(gates)-1])
	}
}

// TestLoadRecipe_WebRoundTrip proves the web recipe round-trips: GenerateDraft on a
// web repo emits the visual gate into .conductor/config.yaml, and LoadRecipe reads
// the five ordered gates back — build, test, vet, lint, visual — so the daemon runs
// the SAME visual-diff gate the scaffolder drafted (ADR-0023).
func TestLoadRecipe_WebRoundTrip(t *testing.T) {
	draft, err := GenerateDraft(fixture(t, "web-repo"), "develop")
	if err != nil {
		t.Fatalf("GenerateDraft: %v", err)
	}
	if draft.Detection.Primary != StackWeb {
		t.Fatalf("primary stack = %q, want web", draft.Detection.Primary)
	}
	if !draft.Readiness.Ready {
		t.Fatalf("web repo with a test script should be READY: %s", draft.Readiness.Reason)
	}
	dir := t.TempDir()
	confDir := filepath.Join(dir, ".conductor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "config.yaml"), draft.Config, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	rec, err := LoadRecipe(dir)
	if err != nil {
		t.Fatalf("LoadRecipe: %v", err)
	}
	want := []GateSpec{
		{Name: "build", Argv: []string{"npm", "run", "build"}},
		{Name: "test", Argv: []string{"npm", "test"}},
		{Name: "vet", Argv: []string{"npm", "run", "typecheck"}},
		{Name: "lint", Argv: []string{"npx", "eslint", "."}},
		{Name: "visual", Argv: []string{"imagediff", VisualActualPath, VisualReferencePath, "-threshold", DefaultVisualThreshold}},
	}
	if !reflect.DeepEqual(rec.Gates, want) {
		t.Fatalf("web gates = %+v, want %+v", rec.Gates, want)
	}
}

// TestLoadRecipe_CustomDevelop proves LoadRecipe surfaces a human-confirmed develop
// command (not the placeholder) verbatim — the per-project performer.
func TestLoadRecipe_CustomDevelop(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, ".conductor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := "version: 1\nstack: node\nbase_branch: develop\nrecipe:\n" +
		"  develop: [my-performer, --task]\n  verify:\n    test: [node, --test]\n"
	if err := os.WriteFile(filepath.Join(confDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	rec, err := LoadRecipe(dir)
	if err != nil {
		t.Fatalf("LoadRecipe: %v", err)
	}
	if want := []string{"my-performer", "--task"}; !reflect.DeepEqual(rec.Develop, want) {
		t.Fatalf("develop = %v, want %v", rec.Develop, want)
	}
	if want := []GateSpec{{Name: "test", Argv: []string{"node", "--test"}}}; !reflect.DeepEqual(rec.Gates, want) {
		t.Fatalf("gates = %+v, want %+v", rec.Gates, want)
	}
}

// TestLoadRecipe_NoConfig proves a repo with no .conductor/config.yaml returns the
// ErrNoRecipe sentinel so the daemon falls back to its flag/default recipe.
func TestLoadRecipe_NoConfig(t *testing.T) {
	_, err := LoadRecipe(t.TempDir())
	if !errors.Is(err, ErrNoRecipe) {
		t.Fatalf("LoadRecipe(no config): err = %v, want ErrNoRecipe", err)
	}
}

// TestLoadRecipe_Corrupt proves an unparseable config is a HARD error (not a silent
// fallback): a corrupt recipe must not degrade the merge gate.
func TestLoadRecipe_Corrupt(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, ".conductor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "config.yaml"), []byte("recipe: [this is: not valid\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadRecipe(dir)
	if err == nil {
		t.Fatal("LoadRecipe(corrupt): err = nil, want parse error")
	}
	if errors.Is(err, ErrNoRecipe) {
		t.Fatalf("LoadRecipe(corrupt): got ErrNoRecipe, want a hard parse error: %v", err)
	}
}

// TestLoadRecipe_NoGates proves a present config declaring no verify gates is a HARD
// error — never a silent degrade.
func TestLoadRecipe_NoGates(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, ".conductor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := "version: 1\nstack: unknown\nbase_branch: develop\nrecipe:\n  develop: [echo, x]\n"
	if err := os.WriteFile(filepath.Join(confDir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := LoadRecipe(dir)
	if err == nil || errors.Is(err, ErrNoRecipe) {
		t.Fatalf("LoadRecipe(no gates): err = %v, want a hard 'no verify gates' error", err)
	}
}

// goRepoDir builds a minimal Go repo (go.mod + a _test.go) so GenerateDraft picks
// the Go profile and emits its build/test/vet/lint gates.
func goRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write test: %v", err)
	}
	return dir
}
