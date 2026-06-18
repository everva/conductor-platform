package holdout

import (
	"context"
	"strings"
	"testing"
)

func TestRouter_DispatchesByScheme_ToFS(t *testing.T) {
	root := writeHoldout(t, "holdouts/A-1", map[string]string{"holdout_test.go": "package x\n"})
	fs, err := New(root)
	if err != nil {
		t.Fatalf("New fs: %v", err)
	}
	r, err := NewRouter(WithFS(fs))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}

	h, err := r.Fetch(context.Background(), "store://holdouts/A-1/spec.yaml")
	if err != nil {
		t.Fatalf("Fetch store://: %v", err)
	}
	if h.Name != "A-1" || len(h.Files) != 1 {
		t.Fatalf("unexpected holdout: name=%q files=%d", h.Name, len(h.Files))
	}
}

func TestRouter_EmptyRef_SkipsCleanly(t *testing.T) {
	fs, _ := New(t.TempDir())
	r, _ := NewRouter(WithFS(fs))
	h, err := r.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("empty ref must not error: %v", err)
	}
	if len(h.Files) != 0 {
		t.Fatalf("empty ref must yield no files, got %d", len(h.Files))
	}
}

func TestRouter_UnknownScheme_Errors(t *testing.T) {
	fs, _ := New(t.TempDir())
	r, _ := NewRouter(WithFS(fs))
	_, err := r.Fetch(context.Background(), "ftp://nope/here")
	if err == nil {
		t.Fatalf("unknown scheme must error")
	}
	if !strings.Contains(err.Error(), "unrecognized locator") {
		t.Fatalf("want unrecognized-locator error, got %v", err)
	}
}

func TestRouter_UnconfiguredScheme_Errors(t *testing.T) {
	// Only the fs backing is wired: a pg:// or private: ref must be a CLEAR
	// "not configured" error, never a silent skip (Rule#9, no fake-green).
	fs, _ := New(t.TempDir())
	r, _ := NewRouter(WithFS(fs))

	for _, ref := range []string{"pg://holdouts/A-1", "private:repo#holdouts/A-1"} {
		_, err := r.Fetch(context.Background(), ref)
		if err == nil {
			t.Fatalf("unconfigured scheme for %q must error", ref)
		}
		if !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("ref %q: want not-configured error, got %v", ref, err)
		}
	}
}

func TestNewRouter_NoBackings_Errors(t *testing.T) {
	if _, err := NewRouter(); err == nil {
		t.Fatalf("router with no backings must error")
	}
}

func TestRouter_ActiveSchemes(t *testing.T) {
	fs, _ := New(t.TempDir())
	priv, _ := NewPrivate(PrivateConfig{CacheDir: t.TempDir()})
	r, _ := NewRouter(WithFS(fs), WithPrivate(priv))
	got := strings.Join(r.ActiveSchemes(), ",")
	if got != "store,private" {
		t.Fatalf("ActiveSchemes = %q, want store,private", got)
	}
}
