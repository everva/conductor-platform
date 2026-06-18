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
	if id == "" {
		return "", fmt.Errorf("holdout: pg locator %q has no holdout id (want %s%s<id>)", locator, pgScheme, pgLocatorPrefix)
	}
	return id, nil
}
