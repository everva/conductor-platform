package holdout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHoldout creates <root>/<dir>/inject/<rel> = content for every entry,
// returning root. It is the on-disk fixture the FSStore resolves a store:// ref
// against. The root is repo-external by construction (a t.TempDir, never inside
// the project checkout).
func writeHoldout(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(dir), injectSubdir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

func TestFSStore_Fetch_StoreRef_ResolvesFiles(t *testing.T) {
	root := writeHoldout(t, "holdouts/A-1", map[string]string{
		"holdout_test.go":   "package x\n",
		"sub/extra_test.go": "package x\n// extra\n",
	})
	s, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	h, err := s.Fetch(context.Background(), "store://holdouts/A-1/spec.yaml")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if h.Name != "A-1" {
		t.Fatalf("Name = %q, want A-1", h.Name)
	}
	if len(h.Files) != 2 {
		t.Fatalf("Files = %d, want 2: %v", len(h.Files), keys(h.Files))
	}
	if got := string(h.Files["holdout_test.go"]); got != "package x\n" {
		t.Fatalf("holdout_test.go content = %q", got)
	}
	if _, ok := h.Files["sub/extra_test.go"]; !ok {
		t.Fatalf("missing nested file sub/extra_test.go; got %v", keys(h.Files))
	}
}

func TestFSStore_Fetch_EmptyRef_SkipsCleanly(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h, err := s.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("empty ref should not error, got %v", err)
	}
	if len(h.Files) != 0 {
		t.Fatalf("empty ref should yield no files, got %v", keys(h.Files))
	}
}

func TestFSStore_Fetch_MissingDir_Errors(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.Fetch(context.Background(), "store://holdouts/NOPE/spec.yaml"); err == nil {
		t.Fatalf("missing holdout dir must error")
	}
}

func TestFSStore_Fetch_EmptyInjectDir_Errors(t *testing.T) {
	root := t.TempDir()
	// Create the inject dir but leave it empty.
	if err := os.MkdirAll(filepath.Join(root, "holdouts", "E", injectSubdir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s, _ := New(root)
	if _, err := s.Fetch(context.Background(), "store://holdouts/E/spec.yaml"); err == nil {
		t.Fatalf("empty inject dir must error")
	}
}

func TestFSStore_Fetch_UnsupportedSchemes(t *testing.T) {
	s, _ := New(t.TempDir())
	for _, ref := range []string{"pg://holdouts/A-1", "private:holdouts#A-1"} {
		_, err := s.Fetch(context.Background(), ref)
		if err == nil {
			t.Fatalf("scheme %q must be unsupported", ref)
		}
		if !strings.Contains(err.Error(), "unsupported scheme") {
			t.Fatalf("ref %q: want unsupported-scheme error, got %v", ref, err)
		}
	}
}

func TestFSStore_Fetch_UnrecognizedLocator(t *testing.T) {
	s, _ := New(t.TempDir())
	if _, err := s.Fetch(context.Background(), "/etc/passwd"); err == nil {
		t.Fatalf("non-scheme locator must error")
	}
	if _, err := s.Fetch(context.Background(), "store://"); err == nil {
		t.Fatalf("scheme with no body must error")
	}
	if _, err := s.Fetch(context.Background(), "store://barefile"); err == nil {
		t.Fatalf("locator with no holdout directory must error")
	}
}

func TestFSStore_Fetch_PathTraversalLocator_Rejected(t *testing.T) {
	s, _ := New(t.TempDir())
	if _, err := s.Fetch(context.Background(), "store://../../escape/spec.yaml"); err == nil {
		t.Fatalf("locator escaping the root via .. must be rejected")
	}
}

func TestFSStore_Fetch_TraversalInjectFile_Rejected(t *testing.T) {
	// A holdout whose inject/ contains a symlink pointing outside is rejected so a
	// malicious holdout cannot inject outside the verify-worktree (ADR-0018).
	root := t.TempDir()
	injectDir := filepath.Join(root, "holdouts", "S", injectSubdir)
	if err := os.MkdirAll(injectDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := filepath.Join(root, "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(injectDir, "link.txt")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	s, _ := New(root)
	if _, err := s.Fetch(context.Background(), "store://holdouts/S/spec.yaml"); err == nil {
		t.Fatalf("symlink in inject dir must be rejected")
	}
}

func TestNew_EmptyRoot_Errors(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatalf("empty root must error")
	}
	if _, err := New("   "); err == nil {
		t.Fatalf("blank root must error")
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
