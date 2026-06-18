//go:build e2e

// Package conductor e2e (web visual-diff, 2A-2): the DETERMINISTIC proof of the
// ADR-0023 visual-verify GATE — render→diff→exit-code as a recipe gate, NOT a new
// verify-type. A "web" task whose PRODUCED screenshot MATCHES the repo-EXTERNAL
// reference within threshold passes the visual gate and (with the other gates
// green) MERGES; a task whose screenshot DIFFERS beyond threshold FAILS the visual
// gate and is BLOCKED with no merge. The reference image is supplied exactly like
// a hidden holdout (ADR-0018 store://): injected into the throwaway verify-worktree
// at verify time, so the performer never sees it and cannot overfit.
//
// The decision is the `imagediff` tool (cmd/imagediff): stdlib-only PNG pixel diff,
// no browser, no network. This proves the visual gate end-to-end OFFLINE. The
// performer commits the produced PNG into its HEAD; the verify-worktree (cut from
// that HEAD) therefore carries the actual, the holdout injects the reference, and
// the holdout command runs imagediff over both — a single deterministic exit code.
package conductor

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/holdout"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// visualActualRel is where the performer writes (and commits) the produced
// screenshot, matching the web recipe convention (scaffolder.VisualActualPath).
const visualActualRel = ".conductor/visual/actual.png"

// visualReferenceRel is where the holdout injects the repo-external reference,
// matching scaffolder.VisualReferencePath.
const visualReferenceRel = ".conductor/visual/reference.png"

// TestE2E_VisualDiffGate_PassMergesAndFailBlocks runs BOTH halves of the ADR-0023
// proof through the real conductor pipeline (provisioner, CommandEngine performer,
// independent verify gate with the real FSStore holdout, GitMerger):
//
//	PASS: performer produces a screenshot byte-identical to the injected reference
//	      -> visual-diff gate exit 0 -> verify pass -> MERGE.
//	FAIL: performer produces a DIFFERENT screenshot -> visual-diff gate exit non-0
//	      -> verify changes-requested -> retries exhausted -> BLOCKED, no merge.
func TestE2E_VisualDiffGate_PassMergesAndFailBlocks(t *testing.T) {
	requireGit(t)
	requireGo(t)
	ctx := context.Background()

	// Build the real imagediff tool once into a dir we put on PATH for the gate.
	binDir := t.TempDir()
	imagediff := buildImagediff(t, binDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	_ = imagediff

	// The repo-EXTERNAL reference body: a tiny PNG injected as a hidden holdout.
	refPNG := solidPNG(t, 16, 16, color.RGBA{R: 0x20, G: 0x80, B: 0xff, A: 0xff})

	t.Run("match within threshold merges", func(t *testing.T) {
		runVisualScenario(t, ctx, refPNG, refPNG, true)
	})
	t.Run("differ beyond threshold blocks", func(t *testing.T) {
		// A wholly different screenshot -> 100% differing pixels -> beyond threshold.
		actual := solidPNG(t, 16, 16, color.RGBA{R: 0xff, G: 0x00, B: 0x00, A: 0xff})
		runVisualScenario(t, ctx, refPNG, actual, false)
	})
}

// runVisualScenario drives one tick (or retry loop) of a web task: the performer
// writes actualPNG to the produced-screenshot path and commits; the reference
// holdout injects refPNG; the holdout command (imagediff) is the deterministic
// visual gate. wantMerge selects the PASS (merge) vs FAIL (blocked) assertion.
func runVisualScenario(t *testing.T, ctx context.Context, refPNG, actualPNG []byte, wantMerge bool) {
	t.Helper()
	upstream := newProductRepo(t)

	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{ID: e2eProjectID, Repo: upstream, BaseBranch: "develop"})
	mustCreateScenario(t, store, statestore.Scenario{
		ID:         "scn-visual",
		ProjectID:  e2eProjectID,
		Title:      "web visual-diff",
		HoldoutRef: "store://references/web-home/spec.yaml",
	})
	mustCreateTask(t, store, statestore.Task{
		ID:         "T-visual",
		ProjectID:  e2eProjectID,
		Lane:       "web",
		Tier:       "T2",
		Status:     registry.StatusReady,
		ScenarioID: "scn-visual",
	})

	// Repo-external holdout root: inject the reference PNG at the recipe path.
	holdoutRoot := t.TempDir()
	injectDir := filepath.Join(holdoutRoot, "references", "web-home", "inject", filepath.Dir(visualReferenceRel))
	if err := os.MkdirAll(injectDir, 0o755); err != nil {
		t.Fatalf("mkdir inject: %v", err)
	}
	if err := os.WriteFile(filepath.Join(injectDir, filepath.Base(visualReferenceRel)), refPNG, 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}

	// The performer writes the produced screenshot (base64-decoded from its script)
	// to the actual path, commits, and self-reports pass. It is a stand-in for the
	// Playwright render step; the DETERMINISTIC decision is the imagediff holdout.
	performer := writePerformer(t, visualPerformerScript(t, actualPNG))
	root := t.TempDir()
	cond := buildVisualConductor(t, store, root, performer, holdoutRoot)

	res, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("visual tick: %v (outcome=%s review=%+v)", err, res.Outcome, res.Review)
	}
	clone := filepath.Join(root, "clones", e2eProjectID)

	if wantMerge {
		if res.Outcome != OutcomeMerged || res.MergeSHA == "" {
			t.Fatalf("PASS: want merged, got outcome=%s sha=%q review=%+v", res.Outcome, res.MergeSHA, res.Review)
		}
		tip := gitT(t, clone, "log", "-1", "--format=%B", "develop")
		if !strings.Contains(tip, "[task:T-visual]") {
			t.Fatalf("PASS: develop tip missing [task:T-visual] trailer:\n%s", tip)
		}
		t.Logf("VISUAL-PASS: outcome=%s mergeSHA=%s review=%s", res.Outcome, res.MergeSHA[:12], res.Review.Result)
		return
	}

	// FAIL path: never merges; retry until terminal blocked.
	const maxTicks = MaxRetries + 2
	for i := 0; i < maxTicks && res.Outcome == OutcomeRetry; i++ {
		res, err = cond.Tick(ctx, e2eProjectID)
		if err != nil {
			t.Fatalf("visual re-tick %d: %v", i, err)
		}
		if res.MergeSHA != "" {
			t.Fatalf("FAIL: must NOT merge (re-tick %d), got sha=%q", i, res.MergeSHA)
		}
	}
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("FAIL: want blocked, got %s", res.Outcome)
	}
	got := mustGetTask(t, store, "T-visual")
	if got.Status != registry.StatusBlocked {
		t.Fatalf("FAIL: task status = %q, want blocked", got.Status)
	}
	tip := gitT(t, clone, "log", "-1", "--format=%B", "develop")
	if strings.Contains(tip, "[task:T-visual]") {
		t.Fatalf("FAIL: a [task:T-visual] trailer LANDED on develop (fake-green):\n%s", tip)
	}
	t.Logf("VISUAL-FAIL: outcome=%s taskStatus=%s noMerge=ok review=%s", res.Outcome, got.Status, res.Review.Result)
}

// buildVisualConductor wires the real components with the FSStore holdout and the
// VISUAL-DIFF gate as the holdout command: imagediff compares the produced
// screenshot (committed into HEAD, present in the verify-worktree) against the
// injected reference at the recipe threshold. The visible gates (go build/test)
// stay green so the verdict rides on the deterministic visual decision.
func buildVisualConductor(t *testing.T, store *statestore.MemoryStore, root, performer, holdoutRoot string) *Conductor {
	t.Helper()
	prov, err := provisioner.New(provisioner.Config{RootDir: root})
	if err != nil {
		t.Fatalf("provisioner.New: %v", err)
	}
	eng := engine.NewCommandEngine(engine.RecipeConfig{DevelopCmd: []string{performer}, Timeout: 60 * time.Second})

	hstore, err := holdout.New(holdoutRoot)
	if err != nil {
		t.Fatalf("holdout.New: %v", err)
	}
	// The holdout command IS the visual-diff gate: imagediff <actual> <reference>.
	verf := verify.New(hstore, verify.Config{
		HoldoutCmd: []string{"imagediff", visualActualRel, visualReferenceRel, "-threshold", "0.02"},
	})

	merger := NewGitMerger(func(projectID string) string { return filepath.Join(root, "clones", projectID) })

	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: prov,
		Engine:      eng,
		Verifier:    verf,
		Merger:      merger,
		Recipe: Recipe{Gates: []verify.Gate{
			{Name: "go build", Argv: []string{"go", "build", "./..."}},
			{Name: "go test", Argv: []string{"go", "test", "./..."}},
		}},
		HostID: "visual-e2e-host",
	})
	if err != nil {
		t.Fatalf("conductor.New: %v", err)
	}
	return cond
}

// buildImagediff compiles cmd/imagediff into binDir and returns its path so the
// gate (run via PATH) is the REAL deterministic tool, not a stub.
func buildImagediff(t *testing.T, binDir string) string {
	t.Helper()
	out := filepath.Join(binDir, "imagediff")
	cmd := exec.Command("go", "build", "-o", out, "github.com/everva/conductor-platform/cmd/imagediff")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("build imagediff: %v\n%s", err, buf.String())
	}
	return out
}

// solidPNG returns a w×h solid-color PNG as bytes (the deterministic test image).
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

// visualPerformerScript writes a /bin/sh performer that decodes the given PNG
// bytes (base64) into the produced-screenshot path, commits, and self-reports a
// passing Verdict. It stands in for the Playwright render step; the gate decision
// is the deterministic imagediff holdout, never this self-report (Rule#9).
func visualPerformerScript(t *testing.T, pngBytes []byte) string {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	dir := filepath.Dir(visualActualRel)
	return "#!/bin/sh\nset -e\n" +
		"mkdir -p " + shellQuote(dir) + "\n" +
		"printf %s " + shellQuote(b64) + " | base64 -d > " + shellQuote(visualActualRel) + "\n" +
		"export GIT_AUTHOR_NAME=performer GIT_AUTHOR_EMAIL=performer@local\n" +
		"export GIT_COMMITTER_NAME=performer GIT_COMMITTER_EMAIL=performer@local\n" +
		"git add " + shellQuote(visualActualRel) + "\n" +
		"git commit -q -m 'feat: render produced screenshot'\n" +
		"BR=$(git rev-parse --abbrev-ref HEAD)\n" +
		"SHA=$(git rev-parse HEAD)\n" +
		"cat <<EOF\n" +
		"{\"result\":\"pass\",\"branch\":\"$BR\",\"commit_sha\":\"$SHA\",\"checks\":[{\"name\":\"render\",\"result\":\"pass\",\"evidence\":\"screenshot produced\"}],\"files\":[\"" + visualActualRel + "\"],\"summary\":\"rendered screenshot\"}\n" +
		"EOF\n"
}
