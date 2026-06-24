package holdout

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/everva/conductor-platform/internal/verify"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgLocatorPrefix is the holdouts namespace inside a pg:// locator. A pg holdout
// is addressed "pg://holdouts/<id>" (optionally with trailing path elements that
// are ignored, mirroring the store:// locator's "addresses a file in the holdout"
// shape). The id selects every row in the holdouts table with that id.
const pgLocatorPrefix = "holdouts/"

// PGStore is a Postgres-backed verify.HoldoutStore (ADR-0018, Faz-1.5-a). Each
// holdout is a set of rows in the `holdouts` table — (id, path) -> content — that
// live in the central Postgres, repo-EXTERNAL by construction. Construct it with
// NewPG over a pgxpool; it holds only the pool and no other state, so it is safe
// to share across verify runs.
//
// Ref format: "pg://holdouts/<id>". Fetch reads every row for <id>, returning a
// verify.Holdout{Name:<id>, Files: path->content}. A missing id (no rows) is a
// clear error, never a silent empty holdout. Stored paths are validated through
// the shared safeRel guard (".." / absolute rejected) so a malicious row cannot
// inject outside the verify-worktree. Row CONTENTS are read but never logged.
type PGStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that *PGStore satisfies the frozen verify.HoldoutStore.
var _ verify.HoldoutStore = (*PGStore)(nil)

// NewPG returns a PGStore backed by the given pgxpool. The pool is injected (the
// daemon opens it from the same -dsn the statestore uses); PGStore does not own
// it and does not close it. A nil pool is a programming error.
func NewPG(pool *pgxpool.Pool) (*PGStore, error) {
	if pool == nil {
		return nil, errors.New("holdout: pg store needs a non-nil pool")
	}
	return &PGStore{pool: pool}, nil
}

// Fetch resolves a pg:// locator to the holdout rows for its id and returns the
// injectable files (verify.HoldoutStore). The holdouts table is expected to exist
// (the statestore migrations create it when -dsn is set). A missing id, an empty
// id, or a path-traversal row is a clear error rather than a silent skip.
func (s *PGStore) Fetch(ctx context.Context, ref string) (verify.Holdout, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return verify.Holdout{}, nil
	}
	if !strings.HasPrefix(trimmed, pgScheme) {
		return verify.Holdout{}, fmt.Errorf("holdout: pg store given non-pg locator %q", ref)
	}

	id, err := pgHoldoutID(trimmed)
	if err != nil {
		return verify.Holdout{}, err
	}

	const q = `SELECT path, content FROM holdouts WHERE id = $1 ORDER BY path`
	rows, err := s.pool.Query(ctx, q, id)
	if err != nil {
		return verify.Holdout{}, fmt.Errorf("holdout: query pg holdout %q: %w", id, err)
	}
	defer rows.Close()

	files := make(map[string][]byte)
	for rows.Next() {
		var path string
		var content []byte
		if err := rows.Scan(&path, &content); err != nil {
			return verify.Holdout{}, fmt.Errorf("holdout: scan pg holdout %q: %w", id, err)
		}
		rel := strings.TrimSpace(path)
		if serr := safeRel(rel); serr != nil {
			return verify.Holdout{}, fmt.Errorf("holdout: pg holdout %q has unsafe path: %w", id, serr)
		}
		files[rel] = content
	}
	if err := rows.Err(); err != nil {
		return verify.Holdout{}, fmt.Errorf("holdout: read pg holdout %q: %w", id, err)
	}

	if len(files) == 0 {
		return verify.Holdout{}, fmt.Errorf("holdout: no pg holdout rows for id %q (ref %q)", id, ref)
	}
	return verify.Holdout{Name: id, Files: files}, nil
}

// Store writes a holdout's files to the central Postgres under the given id and returns its pg://
// locator (Faz-S — the write path the gateway's PUT /holdouts/{id} uses so an intake-approved,
// auto-generated holdout becomes fetchable by the agent's gate). Each (id, path) is UPSERTed so a
// re-approval replaces the prior body. Paths are validated with the SAME safeRel guard Fetch reads
// through (".." / absolute rejected) so an unsafe path is refused at write time, not just read time.
// The whole set is written in ONE transaction (all-or-nothing). An empty id or empty file set is a
// clear error; file CONTENTS are never logged (ADR-0018). A nil pool would have failed at NewPG.
func (s *PGStore) Store(ctx context.Context, id string, files map[string][]byte) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("holdout: store needs a non-empty id")
	}
	if len(files) == 0 {
		return "", fmt.Errorf("holdout: store %q given no files", id)
	}
	// Validate every path BEFORE opening the transaction so a bad set never partially writes.
	for path := range files {
		rel := strings.TrimSpace(path)
		if rel == "" {
			return "", fmt.Errorf("holdout: store %q has an empty file path", id)
		}
		if err := safeRel(rel); err != nil {
			return "", fmt.Errorf("holdout: store %q has unsafe path %q: %w", id, path, err)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("holdout: begin store %q: %w", id, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful Commit.

	// Replace any prior body for this id so a re-approval is a clean overwrite (not a merge of
	// stale + new files): delete the id's rows, then insert the new set.
	if _, err := tx.Exec(ctx, `DELETE FROM holdouts WHERE id = $1`, id); err != nil {
		return "", fmt.Errorf("holdout: clear store %q: %w", id, err)
	}
	const ins = `INSERT INTO holdouts (id, path, content) VALUES ($1, $2, $3)`
	for path, content := range files {
		if _, err := tx.Exec(ctx, ins, id, strings.TrimSpace(path), content); err != nil {
			return "", fmt.Errorf("holdout: insert store %q: %w", id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("holdout: commit store %q: %w", id, err)
	}
	return pgScheme + pgLocatorPrefix + id, nil
}

// pgHoldoutID extracts the holdout id from a pg:// locator. The locator body must
// be "holdouts/<id>[/...]"; the id is the path element after the holdouts/ prefix.
// Trailing path elements (e.g. a spec file name) are ignored, mirroring the
// store:// locator addressing a file within the holdout.
func pgHoldoutID(locator string) (string, error) {
	body := strings.TrimPrefix(locator, pgScheme)
	body = strings.Trim(body, "/")
	if body == "" {
		return "", fmt.Errorf("holdout: pg locator %q has scheme but no body (want %sholdouts/<id>)", locator, pgScheme)
	}
	if !strings.HasPrefix(body, pgLocatorPrefix) {
		return "", fmt.Errorf("holdout: pg locator %q must address %s%s<id>", locator, pgScheme, pgLocatorPrefix)
	}
	rest := strings.TrimPrefix(body, pgLocatorPrefix)
	id := rest
	if idx := strings.Index(rest, "/"); idx >= 0 {
		id = rest[:idx]
	}
	// L4: TrimSpace the parsed id and validate it is non-empty so a whitespace-only
	// id (e.g. "pg://holdouts/%20" / a padded value) fails with a CLEAR validation
	// error here rather than slipping through to a confusing "no rows for id" later.
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("holdout: pg locator %q has no holdout id (want %s%s<id>)", locator, pgScheme, pgLocatorPrefix)
	}
	return id, nil
}
