// privatestore.go implements the private: holdout backing (ADR-0018, ADR-0017,
// Faz-1.5-a): hidden holdouts that live in a SEPARATE private git repo, cloned
// via a gh-token credential helper (deploy-keys forbidden, ADR-0017). The clone
// is repo-EXTERNAL by construction (it is a different repo than any product
// checkout) and is cached under an operator-supplied cache dir so repeated
// fetches are cheap. The gh-token is injected from the daemon's env; it is NEVER
// logged or committed, and any error that might carry it is redacted.
package holdout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/everva/conductor-platform/internal/verify"
)

// privateRefSep separates the repo from the holdout path inside a private:
// locator: "private:<repo>#<path>". <repo> is a git remote (https URL or a local
// path for tests); <path> is the holdout DIRECTORY within that repo whose inject/
// subdir is injected, mirroring the store:// layout (so a private holdout repo is
// laid out identically to a filesystem holdout root).
const privateRefSep = "#"

// PrivateRepoStore is a private-git-repo-backed verify.HoldoutStore. It clones the
// referenced repo (shallow) into a per-repo cache dir under CacheDir using a
// gh-token credential helper, then reads the holdout files under "<path>/inject/".
// Construct it with NewPrivate.
//
// Ref format: "private:<repo>#<path>" — e.g.
// "private:https://github.com/acme/holdouts.git#holdouts/A-1". The repo is cloned
// once and refreshed on subsequent fetches; <path> selects the holdout directory
// whose inject/ contents are injected. A missing path/inject dir, or an unsafe
// stored path, is a clear error. The token is redacted from every error.
type PrivateRepoStore struct {
	cacheDir string
	ghToken  string
	// allowLocalRemote, when true, SKIPS the S-4 remote allowlist so a local-path
	// repo may be cloned. It is set ONLY by in-package tests that exercise the read
	// path against a throwaway local git repo; NewPrivate never sets it, so
	// production always enforces the https/ssh-only allowlist.
	allowLocalRemote bool
}

// Compile-time assertion that *PrivateRepoStore satisfies verify.HoldoutStore.
var _ verify.HoldoutStore = (*PrivateRepoStore)(nil)

// PrivateConfig configures a PrivateRepoStore. CacheDir is the directory cloned
// holdout repos are cached under (created if absent). GHToken is the gh-token the
// credential helper uses for writable/private HTTPS auth (ADR-0017); empty
// disables the helper (e.g. a local-path test repo that needs no auth). The token
// is never logged or committed.
type PrivateConfig struct {
	CacheDir string
	GHToken  string
}

// NewPrivate returns a PrivateRepoStore. CacheDir is required (the cache root must
// be injected explicitly, no global singleton); it is created if absent.
func NewPrivate(cfg PrivateConfig) (*PrivateRepoStore, error) {
	if strings.TrimSpace(cfg.CacheDir) == "" {
		return nil, errors.New("holdout: private store needs a CacheDir")
	}
	abs, err := filepath.Abs(cfg.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("holdout: resolve private cache dir %q: %w", cfg.CacheDir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("holdout: create private cache dir: %w", err)
	}
	return &PrivateRepoStore{cacheDir: abs, ghToken: cfg.GHToken}, nil
}

// HasToken reports whether a gh-token is configured. It exposes only the boolean
// presence of the token — never its value — so the daemon can log whether the
// private backing has auth without leaking the secret.
func (s *PrivateRepoStore) HasToken() bool { return strings.TrimSpace(s.ghToken) != "" }

// Fetch resolves a private: locator: it clones/refreshes the referenced repo into
// the cache and reads the holdout files under "<path>/inject/". An empty ref skips
// cleanly. A malformed locator, a missing inject dir, or an unsafe stored path is
// a clear (token-redacted) error.
func (s *PrivateRepoStore) Fetch(ctx context.Context, ref string) (verify.Holdout, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return verify.Holdout{}, nil
	}
	if !strings.HasPrefix(trimmed, privateScheme) {
		return verify.Holdout{}, fmt.Errorf("holdout: private store given non-private locator %q", ref)
	}

	repo, holdoutPath, err := parsePrivateRef(trimmed)
	if err != nil {
		return verify.Holdout{}, err
	}

	// Scheme/remote allowlist (S-4): the repo MUST be a safe https/ssh git remote.
	// file://, absolute local paths, and "../"-traversal are rejected so a holdout
	// ref can never clone an arbitrary LOCAL directory (limited SSRF / local-fs read).
	// The only exception is the unexported test allowance (allowLocalRemote), set
	// solely by in-package tests that exercise the read path against a throwaway
	// local git repo; production construction (NewPrivate) never sets it.
	if !s.allowLocalRemote {
		if verr := validatePrivateRemote(repo); verr != nil {
			return verify.Holdout{}, fmt.Errorf("holdout: private locator %q: %w", ref, verr)
		}
	}

	clone, err := s.ensureClone(ctx, repo)
	if err != nil {
		return verify.Holdout{}, s.redact(err)
	}

	// Defense-in-depth: the holdout path must not escape the clone via "..".
	cleanPath := filepath.Clean(holdoutPath)
	if cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(os.PathSeparator)) || filepath.IsAbs(cleanPath) {
		return verify.Holdout{}, fmt.Errorf("holdout: private locator %q escapes the repo", ref)
	}

	injectDir := filepath.Join(clone, filepath.FromSlash(cleanPath), injectSubdir)
	info, statErr := os.Stat(injectDir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return verify.Holdout{}, fmt.Errorf("holdout: no inject dir for %q at <repo>/%s/%s", ref, cleanPath, injectSubdir)
		}
		return verify.Holdout{}, fmt.Errorf("holdout: stat inject dir for %q: %w", ref, s.redact(statErr))
	}
	if !info.IsDir() {
		return verify.Holdout{}, fmt.Errorf("holdout: inject path for %q is not a directory", ref)
	}

	files, err := readInjectFiles(injectDir)
	if err != nil {
		return verify.Holdout{}, fmt.Errorf("holdout: read %q: %w", ref, s.redact(err))
	}
	if len(files) == 0 {
		return verify.Holdout{}, fmt.Errorf("holdout: inject dir for %q is empty", ref)
	}

	return verify.Holdout{Name: holdoutName(cleanPath, ""), Files: files}, nil
}

// ensureClone clones repo into a per-repo cache dir on first use and refreshes it
// (fetch + reset to the fetched HEAD) on subsequent fetches, so a private holdout
// repo is cloned once and kept current. It uses a shallow clone and the gh-token
// credential helper (never a deploy-key, ADR-0017). The returned path is the
// clone working tree.
func (s *PrivateRepoStore) ensureClone(ctx context.Context, repo string) (string, error) {
	dest := filepath.Join(s.cacheDir, cacheKey(repo))

	if isGitRepoDir(dest) {
		// Refresh: fetch the remote default and hard-reset so the cache is current.
		if err := s.configureAuth(ctx, dest); err != nil {
			return "", err
		}
		if err := runGitEnv(ctx, dest, s.gitEnv(), "fetch", "--depth", "1", "origin", "HEAD"); err != nil {
			return "", err
		}
		if err := runGitEnv(ctx, dest, s.gitEnv(), "reset", "--hard", "FETCH_HEAD"); err != nil {
			return "", err
		}
		return dest, nil
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("prepare private clone dir: %w", err)
	}
	if err := runGitEnv(ctx, s.cacheDir, s.gitEnv(), "clone", "--depth", "1", "--origin", "origin", repo, dest); err != nil {
		return "", err
	}
	if err := s.configureAuth(ctx, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// configureAuth installs a gh-token credential helper on the clone (ADR-0017,
// reusing the provisioner's pattern). With no token it is a no-op (local-path
// repos need no auth). The token is written only into the clone's LOCAL git
// config (under the repo-external cache dir), never into tracked content.
func (s *PrivateRepoStore) configureAuth(ctx context.Context, clone string) error {
	if strings.TrimSpace(s.ghToken) == "" {
		return nil
	}
	helper := fmt.Sprintf("!f() { echo \"username=x-access-token\"; echo \"password=%s\"; }; f", s.ghToken)
	if err := runGitEnv(ctx, clone, s.gitEnv(), "config", "--local", "credential.helper", helper); err != nil {
		return fmt.Errorf("configure gh-token credential helper: %w", err)
	}
	return nil
}

// gitEnv returns a deterministic env for git that never prompts interactively and
// never injects ssh/deploy-key config (ADR-0017). The gh-token is delivered via
// the credential helper, never via the environment, so it does not appear here.
func (s *PrivateRepoStore) gitEnv() []string {
	return append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
}

// redact removes the gh-token from an error message so a git error that echoed
// the token (e.g. in a URL) never surfaces or is logged. With no token it returns
// the error unchanged.
func (s *PrivateRepoStore) redact(err error) error {
	if err == nil {
		return nil
	}
	tok := strings.TrimSpace(s.ghToken)
	if tok == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), tok, "x-access-token:REDACTED")
	return errors.New(msg)
}

// parsePrivateRef splits "private:<repo>#<path>" into its repo and holdout path.
// Both parts are required; a missing "#" or an empty side is a clear error.
func parsePrivateRef(locator string) (repo, path string, err error) {
	body := strings.TrimPrefix(locator, privateScheme)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", fmt.Errorf("holdout: private locator %q has scheme but no body (want %s<repo>%s<path>)", locator, privateScheme, privateRefSep)
	}
	idx := strings.LastIndex(body, privateRefSep)
	if idx < 0 {
		return "", "", fmt.Errorf("holdout: private locator %q missing %q separator (want %s<repo>%s<path>)", locator, privateRefSep, privateScheme, privateRefSep)
	}
	repo = strings.TrimSpace(body[:idx])
	path = strings.Trim(strings.TrimSpace(body[idx+len(privateRefSep):]), "/")
	if repo == "" {
		return "", "", fmt.Errorf("holdout: private locator %q has empty repo", locator)
	}
	if path == "" {
		return "", "", fmt.Errorf("holdout: private locator %q has empty holdout path", locator)
	}
	return repo, path, nil
}

// validatePrivateRemote restricts the repo part of a private: locator to a SAFE
// remote (S-4): an https/ssh git remote only. It REJECTS file:// URLs, absolute
// local paths, and any "../"-style relative path, which would let a holdout ref
// clone an arbitrary LOCAL directory (limited SSRF / local-fs read) rather than the
// intended out-of-repo private git repo (ADR-0017/0018: holdouts are repo-EXTERNAL
// and fetched over the gh-token HTTPS path, never a deploy-key or a local file).
//
// Accepted shapes:
//   - "https://host/owner/repo.git"  (the gh-token credential-helper path)
//   - "git@host:owner/repo.git" / "ssh://git@host/owner/repo.git" (ssh remotes)
//
// Rejected: "file://...", a leading "/" (absolute path), a Windows drive path, and
// any path containing a ".." segment. Tests that need a local-path repo use the
// store:// filesystem backing, not private:.
func validatePrivateRemote(repo string) error {
	r := strings.TrimSpace(repo)
	low := strings.ToLower(r)

	// Explicitly reject the local-file scheme.
	if strings.HasPrefix(low, "file://") {
		return errors.New("repo uses file:// (local clone forbidden; use an https/ssh git remote)")
	}
	// Reject any ".." traversal segment regardless of shape.
	if r == ".." || strings.HasPrefix(r, "../") || strings.Contains(r, "/../") || strings.HasSuffix(r, "/..") {
		return errors.New("repo contains a '..' path segment (forbidden)")
	}
	// Reject absolute local paths (POSIX leading slash or a Windows drive letter).
	if strings.HasPrefix(r, "/") || isWindowsAbs(r) {
		return errors.New("repo is an absolute local path (local clone forbidden; use an https/ssh git remote)")
	}

	// Accept the explicit safe remote shapes.
	switch {
	case strings.HasPrefix(low, "https://"):
		return nil
	case strings.HasPrefix(low, "ssh://"):
		return nil
	case isScpLikeSSH(r): // git@host:owner/repo.git
		return nil
	default:
		return errors.New("repo must be an https:// or ssh (git@host:...) git remote")
	}
}

// isScpLikeSSH reports whether repo is an scp-like ssh remote ("user@host:path"),
// the common "git@github.com:owner/repo.git" form. It requires a "@" before the
// first ":" and a non-empty host and path, so a bare local path with a colon is not
// mistaken for an ssh remote.
func isScpLikeSSH(repo string) bool {
	at := strings.Index(repo, "@")
	colon := strings.Index(repo, ":")
	if at <= 0 || colon <= at+1 {
		return false
	}
	host := repo[at+1 : colon]
	path := repo[colon+1:]
	return host != "" && path != "" && !strings.Contains(host, "/")
}

// isWindowsAbs reports whether repo looks like an absolute Windows path
// ("C:\\..." or "C:/...") so it is rejected as a local clone target.
func isWindowsAbs(repo string) bool {
	if len(repo) < 3 {
		return false
	}
	c := repo[0]
	isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	return isLetter && repo[1] == ':' && (repo[2] == '\\' || repo[2] == '/')
}

// cacheKey maps a repo remote to a stable, filesystem-safe, COLLISION-FREE per-repo
// cache directory name (L3). It hashes the FULL remote string with sha256 so two
// distinct remotes can never collide onto the same cache dir — a lossy slug (e.g.
// replacing every unsafe char with '_') mapped "git@h:a/r.git" and "git@h/a/r.git"
// (or "https://h/r" and "https__h_r") onto identical dirs, so one repo's clone could
// serve another's holdout. A short human-readable slug PREFIX is kept for diagnosis,
// with the hex digest as the disambiguating, never-colliding suffix. It never embeds
// a token (the token is delivered via the credential helper, not the URL).
func cacheKey(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	digest := hex.EncodeToString(sum[:])

	var b strings.Builder
	for _, r := range repo {
		if b.Len() >= 32 { // cap the human-readable prefix; the digest guarantees uniqueness.
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	slug := b.String()
	if slug == "" {
		slug = "repo"
	}
	return slug + "-" + digest
}

// isGitRepoDir reports whether dir is an existing git repository (a .git dir or,
// for some clones, a .git file).
func isGitRepoDir(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// runGitEnv runs a git command in dir with env, wrapping failures with the
// combined output. The caller (Fetch) redacts the token from the returned error.
func runGitEnv(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}
