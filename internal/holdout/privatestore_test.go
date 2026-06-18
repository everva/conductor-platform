package holdout

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeLocalHoldoutRepo creates a throwaway LOCAL git repo that plays the role of
// the "private" holdout source: clone reads it over a file path, no real GitHub,
// no token. It writes <dir>/inject/<rel>=content for each entry, commits, and
// returns the repo path. The clone path (a file:// URL is unnecessary; git clones
// a bare path fine) proves the read path independently of any auth.
func makeLocalHoldoutRepo(t *testing.T, holdoutDir string, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("checkout", "-q", "-b", "main")
	for rel, content := range files {
		p := filepath.Join(repo, filepath.FromSlash(holdoutDir), injectSubdir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	run("add", "-A")
	run("commit", "-q", "-m", "holdout")
	return repo
}

func TestPrivateRepoStore_Fetch_ClonesAndReads(t *testing.T) {
	repo := makeLocalHoldoutRepo(t, "holdouts/A-1", map[string]string{
		"holdout_test.go":   "package x\n",
		"sub/extra_test.go": "package x\n",
	})
	s, err := NewPrivate(PrivateConfig{CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewPrivate: %v", err)
	}
	s.allowLocalRemote = true // test-only: clone a throwaway local repo (S-4 allowlist skipped).

	ref := "private:" + repo + "#holdouts/A-1"
	h, err := s.Fetch(context.Background(), ref)
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
		t.Fatalf("content = %q", got)
	}
	if _, ok := h.Files["sub/extra_test.go"]; !ok {
		t.Fatalf("missing nested file; got %v", keys(h.Files))
	}

	// Second fetch hits the cache + refresh path (clone already exists).
	if _, err := s.Fetch(context.Background(), ref); err != nil {
		t.Fatalf("second Fetch (cached): %v", err)
	}
}

func TestPrivateRepoStore_Fetch_MissingPath_Errors(t *testing.T) {
	repo := makeLocalHoldoutRepo(t, "holdouts/A-1", map[string]string{"x_test.go": "package x\n"})
	s, _ := NewPrivate(PrivateConfig{CacheDir: t.TempDir()})
	s.allowLocalRemote = true // test-only: clone a throwaway local repo (S-4 allowlist skipped).
	if _, err := s.Fetch(context.Background(), "private:"+repo+"#holdouts/NOPE"); err == nil {
		t.Fatalf("missing holdout path must error")
	}
}

func TestPrivateRepoStore_Fetch_EmptyRef_Skips(t *testing.T) {
	s, _ := NewPrivate(PrivateConfig{CacheDir: t.TempDir()})
	h, err := s.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("empty ref must not error: %v", err)
	}
	if len(h.Files) != 0 {
		t.Fatalf("empty ref must yield no files")
	}
}

func TestParsePrivateRef(t *testing.T) {
	repo, path, err := parsePrivateRef("private:https://github.com/acme/holdouts.git#holdouts/A-1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if repo != "https://github.com/acme/holdouts.git" {
		t.Fatalf("repo = %q", repo)
	}
	if path != "holdouts/A-1" {
		t.Fatalf("path = %q", path)
	}
	// Malformed: no separator, empty sides.
	for _, bad := range []string{"private:onlyrepo", "private:#path", "private:repo#", "private:"} {
		if _, _, err := parsePrivateRef(bad); err == nil {
			t.Fatalf("ref %q must be rejected", bad)
		}
	}
}

func TestParsePrivateRef_WithFragmentInURL(t *testing.T) {
	// LastIndex of "#" means a repo with no fragment + a path after the final "#".
	repo, path, err := parsePrivateRef("private:git@example.com:acme/holdouts.git#holdouts/B-2/spec")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if repo != "git@example.com:acme/holdouts.git" || path != "holdouts/B-2/spec" {
		t.Fatalf("repo=%q path=%q", repo, path)
	}
}

func TestPrivateRepoStore_Redact(t *testing.T) {
	s, _ := NewPrivate(PrivateConfig{CacheDir: t.TempDir(), GHToken: "ghp_supersecret"})
	if !s.HasToken() {
		t.Fatalf("HasToken should be true")
	}
	redacted := s.redact(&stubErr{msg: "git failed for https://x-access-token:ghp_supersecret@github.com/x"})
	if strings.Contains(redacted.Error(), "ghp_supersecret") {
		t.Fatalf("token leaked in redacted error: %v", redacted)
	}
	if !strings.Contains(redacted.Error(), "REDACTED") {
		t.Fatalf("expected REDACTED marker, got %v", redacted)
	}
}

func TestPrivateRepoStore_NoCacheDir_Errors(t *testing.T) {
	if _, err := NewPrivate(PrivateConfig{}); err == nil {
		t.Fatalf("empty CacheDir must error")
	}
}

// TestPrivateRepoStore_Fetch_RejectsUnsafeRemote proves S-4: a private: ref whose
// repo is a local-file scheme, an absolute path, or a "../" traversal is REJECTED at
// Fetch (without the test allowance), while a real https remote is accepted by the
// allowlist (it fails LATER at clone time for an unreachable host, not at the guard).
func TestPrivateRepoStore_Fetch_RejectsUnsafeRemote(t *testing.T) {
	s, _ := NewPrivate(PrivateConfig{CacheDir: t.TempDir()}) // allowLocalRemote = false (production).

	rejected := []string{
		"private:file:///etc#x",
		"private:/abs#x",
		"private:../x#y",
		"private:../../etc/passwd#p",
		"private:./local#p",         // relative local path, no scheme.
		"private:/tmp/holdouts#A-1", // absolute path
	}
	for _, ref := range rejected {
		if _, err := s.Fetch(context.Background(), ref); err == nil {
			t.Fatalf("unsafe remote %q must be rejected by the S-4 allowlist", ref)
		}
	}

	// An https remote PASSES the allowlist guard. It fails afterwards at clone time
	// (unreachable .invalid host), proving the guard accepted it rather than blocking.
	if _, err := s.Fetch(context.Background(), "private:https://nonexistent.invalid/r.git#holdouts/A-1"); err == nil {
		t.Fatalf("https remote should pass the guard and only fail at clone time")
	}
}

// TestValidatePrivateRemote unit-checks the S-4 allowlist directly: https/ssh
// accepted; file://, absolute, and traversal rejected.
func TestValidatePrivateRemote(t *testing.T) {
	accept := []string{
		"https://github.com/acme/holdouts.git",
		"git@github.com:acme/holdouts.git",
		"ssh://git@github.com/acme/holdouts.git",
	}
	for _, r := range accept {
		if err := validatePrivateRemote(r); err != nil {
			t.Fatalf("validatePrivateRemote(%q) = %v, want accept", r, err)
		}
	}
	reject := []string{
		"file:///etc",
		"/abs/path",
		"../x",
		"a/../b",
		"C:\\Users\\x",
		"./relative",
		"plainword",
	}
	for _, r := range reject {
		if err := validatePrivateRemote(r); err == nil {
			t.Fatalf("validatePrivateRemote(%q) = nil, want reject", r)
		}
	}
}

// TestCacheKey_DistinctRemotesDistinctDirs proves L3: distinct remotes that a lossy
// slug would have collided onto one dir now map to DISTINCT cache dirs (the sha256
// suffix disambiguates), so one repo's clone can never serve another's holdout.
func TestCacheKey_DistinctRemotesDistinctDirs(t *testing.T) {
	// These pairs all slug to the SAME lossy form (every unsafe char -> '_'), so the
	// old slug-only key collided; the hashed key must keep them distinct.
	pairs := [][2]string{
		{"git@h:a/r.git", "git@h/a/r.git"},
		{"https://h/a/r", "https://h-a-r"},
		{"https://host/x", "https://host_x"},
		{"a/b", "a_b"},
	}
	for _, p := range pairs {
		k0, k1 := cacheKey(p[0]), cacheKey(p[1])
		if k0 == k1 {
			t.Fatalf("cacheKey collision: %q and %q both -> %q", p[0], p[1], k0)
		}
	}
	// Same remote -> same (stable) key across calls (computed separately to avoid a
	// trivially-identical comparison the linter flags).
	const sameRemote = "https://h/r.git"
	first := cacheKey(sameRemote)
	second := cacheKey(sameRemote)
	if first != second {
		t.Fatalf("cacheKey must be stable for the same remote: %q != %q", first, second)
	}
}

type stubErr struct{ msg string }

func (e *stubErr) Error() string { return e.msg }
