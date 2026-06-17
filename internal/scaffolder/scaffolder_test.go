package scaffolder

import (
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
	want := []Stack{StackGo, StackNode, StackPython, StackRust}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SupportedStacks() = %v, want %v", got, want)
	}
}
