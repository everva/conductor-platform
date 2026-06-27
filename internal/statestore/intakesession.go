package statestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// IntakeMessage is one turn in an intake conversation: "you" (the director) or "assistant" (the
// distiller's reply). Tone is an optional UI hint ("warn" | "error") on an assistant reply.
// Token-free — only human-authored / gateway-derived text crosses here.
type IntakeMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

// IntakeSession is a PERSISTED intake conversation (the "describe → distill → review" chat) so the
// director can reopen prior conversations per project — the Claude-Code-style history the user
// asked for. The chat was EPHEMERAL client state, lost on reload/project-switch; this is OPT-IN
// persistence the director chose (the distiller itself still logs nothing).
//
// ADDITIVE (ADR-0021): this type + the IntakeSessionStore seam are SEPARATE from the frozen
// StateStore, reached by an optional type-assertion, so no existing implementer/fake breaks.
type IntakeSession struct {
	// ID is the conversation id (web-minted, stable across the conversation's saves).
	ID string
	// ProjectID scopes the conversation to a project (the history list is per project).
	ProjectID string
	// Title is a short label derived from the first director turn (for the history list).
	Title string
	// Messages is the full conversation thread, oldest-first. Omitted (nil) in list summaries.
	Messages []IntakeMessage
	// Result is the last authored intake YAML (optional), so a reopened session can resume
	// editing where it left off. Empty when the conversation never produced a draft.
	Result string
	// CreatedAt is when the conversation began; orders the history list (newest first). The store
	// stamps it on the FIRST save when zero, and preserves it across later saves.
	CreatedAt time.Time
	// UpdatedAt is the last save time (set by the store on every save).
	UpdatedAt time.Time
}

// IntakeSessionStore is the OPTIONAL narrow persistence seam for intake conversation history, kept
// SEPARATE from the frozen StateStore (ADR-0021 additive). Both PostgresStore and MemoryStore
// implement it; the gateway type-asserts it for the director endpoints (501 when absent).
type IntakeSessionStore interface {
	// PutIntakeSession UPSERTS a conversation by id: the first save inserts (stamping CreatedAt
	// from the session or now()), later saves update title/messages/result and bump UpdatedAt
	// while PRESERVING the original CreatedAt and ProjectID.
	PutIntakeSession(ctx context.Context, sess IntakeSession) error
	// ListIntakeSessions returns a project's conversations newest-first as SUMMARIES — Messages
	// omitted to keep the list light; Result is kept so the UI can flag one that produced a draft.
	ListIntakeSessions(ctx context.Context, projectID string) ([]IntakeSession, error)
	// GetIntakeSession returns the FULL conversation (with Messages) by id, or a wrapped ErrNotFound.
	GetIntakeSession(ctx context.Context, id string) (IntakeSession, error)
}

// Compile-time assertions that both stores satisfy the additive seam.
var (
	_ IntakeSessionStore = (*MemoryStore)(nil)
	_ IntakeSessionStore = (*PostgresStore)(nil)
)

// cloneIntakeSession deep-copies a session so a stored value never aliases a caller's slice.
// IntakeMessage holds only strings, so copying the slice header + elements is a full deep copy.
func cloneIntakeSession(sess IntakeSession) IntakeSession {
	c := sess
	if sess.Messages != nil {
		c.Messages = make([]IntakeMessage, len(sess.Messages))
		copy(c.Messages, sess.Messages)
	}
	return c
}

// ── MemoryStore ───────────────────────────────────────────────────────────────────────

// PutIntakeSession upserts a conversation (first save stamps CreatedAt; later saves preserve it).
func (s *MemoryStore) PutIntakeSession(ctx context.Context, sess IntakeSession) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sess.ID == "" || sess.ProjectID == "" {
		return fmt.Errorf("put intake session: %w: empty id/project", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if existing, ok := s.intakeSessions[sess.ID]; ok {
		// Preserve the conversation's identity across saves.
		sess.CreatedAt = existing.CreatedAt
		sess.ProjectID = existing.ProjectID
	} else if sess.CreatedAt.IsZero() {
		sess.CreatedAt = now
	}
	sess.UpdatedAt = now
	s.intakeSessions[sess.ID] = cloneIntakeSession(sess)
	return nil
}

// GetIntakeSession returns the full conversation by id, or a wrapped ErrNotFound.
func (s *MemoryStore) GetIntakeSession(ctx context.Context, id string) (IntakeSession, error) {
	if err := ctx.Err(); err != nil {
		return IntakeSession{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.intakeSessions[id]
	if !ok {
		return IntakeSession{}, fmt.Errorf("get intake session %q: %w", id, ErrNotFound)
	}
	return cloneIntakeSession(sess), nil
}

// ListIntakeSessions returns a project's conversations newest-first as summaries (no Messages).
func (s *MemoryStore) ListIntakeSessions(ctx context.Context, projectID string) ([]IntakeSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []IntakeSession
	for _, sess := range s.intakeSessions {
		if sess.ProjectID != projectID {
			continue
		}
		summary := cloneIntakeSession(sess)
		summary.Messages = nil // the list is a light summary
		out = append(out, summary)
	}
	// Newest first; tie-break by id so the order is deterministic across stores.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ── PostgresStore ─────────────────────────────────────────────────────────────────────

// PutIntakeSession upserts a conversation. On insert created_at is the session's time or now();
// on conflict only title/messages/result/updated_at change, so created_at + project_id persist.
func (s *PostgresStore) PutIntakeSession(ctx context.Context, sess IntakeSession) error {
	if sess.ID == "" || sess.ProjectID == "" {
		return fmt.Errorf("put intake session: %w: empty id/project", ErrInvalid)
	}
	msgs, err := marshalMessages(sess.Messages)
	if err != nil {
		return fmt.Errorf("put intake session %q: %w", sess.ID, err)
	}
	var createdAt *time.Time
	if !sess.CreatedAt.IsZero() {
		t := sess.CreatedAt.UTC()
		createdAt = &t
	}
	const q = `
INSERT INTO intake_sessions (id, project_id, title, messages, result, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, COALESCE($6, now()), now())
ON CONFLICT (id) DO UPDATE SET
  title      = EXCLUDED.title,
  messages   = EXCLUDED.messages,
  result     = EXCLUDED.result,
  updated_at = now()`
	if _, err := s.pool.Exec(ctx, q, sess.ID, sess.ProjectID, sess.Title, msgs, sess.Result, createdAt); err != nil {
		return fmt.Errorf("put intake session %q: %w", sess.ID, err)
	}
	return nil
}

// GetIntakeSession returns the full conversation by id, or a wrapped ErrNotFound.
func (s *PostgresStore) GetIntakeSession(ctx context.Context, id string) (IntakeSession, error) {
	const q = `SELECT id, project_id, title, messages, result, created_at, updated_at FROM intake_sessions WHERE id = $1`
	sess, err := scanIntakeSession(s.pool.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return IntakeSession{}, fmt.Errorf("get intake session %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return IntakeSession{}, fmt.Errorf("get intake session %q: %w", id, err)
	}
	return sess, nil
}

// ListIntakeSessions returns a project's conversations newest-first as summaries (no messages).
func (s *PostgresStore) ListIntakeSessions(ctx context.Context, projectID string) ([]IntakeSession, error) {
	const q = `SELECT id, project_id, title, result, created_at, updated_at
FROM intake_sessions WHERE project_id = $1 ORDER BY created_at DESC, id ASC`
	rows, err := s.pool.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("list intake sessions %q: %w", projectID, err)
	}
	defer rows.Close()
	var out []IntakeSession
	for rows.Next() {
		var sess IntakeSession
		if err := rows.Scan(&sess.ID, &sess.ProjectID, &sess.Title, &sess.Result, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list intake sessions %q: %w", projectID, err)
		}
		sess.CreatedAt = sess.CreatedAt.UTC()
		sess.UpdatedAt = sess.UpdatedAt.UTC()
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list intake sessions %q: %w", projectID, err)
	}
	return out, nil
}

// scanIntakeSession reads a full session row, decoding the jsonb messages column.
func scanIntakeSession(r rowScanner) (IntakeSession, error) {
	var sess IntakeSession
	var msgs []byte
	if err := r.Scan(&sess.ID, &sess.ProjectID, &sess.Title, &msgs, &sess.Result, &sess.CreatedAt, &sess.UpdatedAt); err != nil {
		return IntakeSession{}, err
	}
	var err error
	if sess.Messages, err = unmarshalMessages(msgs); err != nil {
		return IntakeSession{}, err
	}
	sess.CreatedAt = sess.CreatedAt.UTC()
	sess.UpdatedAt = sess.UpdatedAt.UTC()
	return sess, nil
}

// marshalMessages encodes the conversation thread to JSON for the jsonb column (empty → "[]").
func marshalMessages(v []IntakeMessage) ([]byte, error) {
	if len(v) == 0 {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal intake messages: %w", err)
	}
	return b, nil
}

// unmarshalMessages decodes the jsonb messages array (empty array → nil, mirroring MemoryStore).
func unmarshalMessages(b []byte) ([]IntakeMessage, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var v []IntakeMessage
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("unmarshal intake messages: %w", err)
	}
	if len(v) == 0 {
		return nil, nil
	}
	return v, nil
}
