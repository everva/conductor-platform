// Package agentclient is the host-agent's typed HTTP client for the conductor-api
// gateway agent-API (ADR-0048, Faz A). It is how a performer host gets work and
// reports results WITHOUT ever opening Postgres: every call is an authenticated HTTP
// request to the gateway, which owns the store. The bearer token is sent ONLY in the
// Authorization header and is never logged or placed in an error message.
package agentclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the gateway agent-API. Construct it with New.
type Client struct {
	base  string
	token string
	hc    *http.Client
}

// New returns a Client for the gateway at baseURL using the given bearer token. A
// trailing slash on baseURL is trimmed. The default HTTP client has a 60s timeout;
// override it with WithHTTPClient for long-poll/streaming needs.
func New(baseURL, token string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, hc: &http.Client{Timeout: 60 * time.Second}}
}

// WithHTTPClient overrides the HTTP client (e.g. a custom timeout/transport).
func (c *Client) WithHTTPClient(hc *http.Client) *Client {
	c.hc = hc
	return c
}

// Error is a non-2xx gateway response: the HTTP status + the gateway's secret-free
// {error} message. It never carries the token or the request body.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("agentclient: gateway %d: %s", e.Status, e.Message)
}

// --- wire types (mirror the gateway agent-API DTOs) ---

// TaskInfo is a leased task.
type TaskInfo struct {
	ID         string   `json:"id"`
	ProjectID  string   `json:"project_id"`
	Lane       string   `json:"lane"`
	Tier       string   `json:"tier"`
	Status     string   `json:"status"`
	Requires   []string `json:"requires"`
	Deps       []string `json:"deps"`
	Branch     string   `json:"branch"`
	ScenarioID string   `json:"scenario_id"`
}

// LeaseInfo is the acquired lease record.
type LeaseInfo struct {
	ProjectID  string    `json:"project_id"`
	HostID     string    `json:"host_id"`
	TaskID     string    `json:"task_id"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// LeasedTask is the lease response (task + lease).
type LeasedTask struct {
	Task  TaskInfo  `json:"task"`
	Lease LeaseInfo `json:"lease"`
}

// ScenarioInfo is a scenario's reviewable detail (acceptance + holdout ref).
type ScenarioInfo struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Lane       string   `json:"lane"`
	Tier       string   `json:"tier"`
	Deps       []string `json:"deps"`
	Acceptance []string `json:"acceptance"`
	HoldoutRef string   `json:"hidden_holdout_ref"`
}

// Check is one gate check in a result report.
type Check struct {
	Name     string `json:"name"`
	Result   string `json:"result"`
	Evidence string `json:"evidence"`
}

// ResultReport is the agent's verdict for a task.
type ResultReport struct {
	Result  string  `json:"result"` // pass | changes-requested | blocked
	Branch  string  `json:"branch"`
	Summary string  `json:"summary"`
	Checks  []Check `json:"checks"`
}

// --- requests ---

type leaseRequest struct {
	HostID       string   `json:"host_id"`
	Capabilities []string `json:"capabilities"`
}
type heartbeatRequest struct {
	HostID string `json:"host_id"`
}
type releaseRequest struct {
	HostID string `json:"host_id"`
	TaskID string `json:"task_id"`
}
type reportRequest struct {
	Phase   string         `json:"phase"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}
type mergedRequest struct {
	SHA string `json:"sha"`
}
type putCredentialRequest struct {
	Token string `json:"token"`
}

// Lease asks the gateway for the next ready task for the project, self-registering
// the host with its capabilities. ok=false means there is no work right now (the
// gateway returned 204, or another host raced us to the lease) — the agent retries.
func (c *Client) Lease(ctx context.Context, projectID, hostID string, capabilities []string) (LeasedTask, bool, error) {
	var lt LeasedTask
	status, err := c.do(ctx, http.MethodPost, "/projects/"+projectID+"/agent/lease", leaseRequest{HostID: hostID, Capabilities: capabilities}, &lt)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusConflict {
			return LeasedTask{}, false, nil // raced — no work this round
		}
		return LeasedTask{}, false, err
	}
	if status == http.StatusNoContent {
		return LeasedTask{}, false, nil // no pickable task
	}
	return lt, true, nil
}

// Heartbeat advances the host's liveness so the reaper does not free its lease.
func (c *Client) Heartbeat(ctx context.Context, hostID string) error {
	_, err := c.do(ctx, http.MethodPost, "/agent/heartbeat", heartbeatRequest{HostID: hostID}, nil)
	return err
}

// Scenarios fetches the project's scenarios (acceptance + holdout refs) so the agent
// can drive develop+verify against the right acceptance.
func (c *Client) Scenarios(ctx context.Context, projectID string) ([]ScenarioInfo, error) {
	var out []ScenarioInfo
	if _, err := c.do(ctx, http.MethodGet, "/projects/"+projectID+"/scenarios", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Report publishes a live develop/verify event (progress/log/diff) for the editor.
func (c *Client) Report(ctx context.Context, projectID, taskID, phase, kind string, payload map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "/projects/"+projectID+"/agent/tasks/"+taskID+"/report",
		reportRequest{Phase: phase, Kind: kind, Payload: payload}, nil)
	return err
}

// Result reports the verdict and returns the gateway's decision: "merge", "hold", or
// "blocked".
func (c *Client) Result(ctx context.Context, projectID, taskID string, report ResultReport) (string, error) {
	var out struct {
		Decision string `json:"decision"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/projects/"+projectID+"/agent/tasks/"+taskID+"/result", report, &out); err != nil {
		return "", err
	}
	return out.Decision, nil
}

// Decision polls the held-task decision: "approved", "aborted", or "pending".
func (c *Client) Decision(ctx context.Context, projectID, taskID string) (string, error) {
	var out struct {
		State string `json:"state"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/projects/"+projectID+"/agent/tasks/"+taskID+"/decision", nil, &out); err != nil {
		return "", err
	}
	return out.State, nil
}

// Merged reports that the agent landed the merge at sha; the gateway marks the task done.
func (c *Client) Merged(ctx context.Context, projectID, taskID, sha string) error {
	_, err := c.do(ctx, http.MethodPost, "/projects/"+projectID+"/agent/tasks/"+taskID+"/merged", mergedRequest{SHA: sha}, nil)
	return err
}

// Release releases the project's lease (owner-scoped, idempotent) when the agent
// finishes or abandons a task.
func (c *Client) Release(ctx context.Context, projectID, hostID, taskID string) error {
	_, err := c.do(ctx, http.MethodPost, "/projects/"+projectID+"/agent/lease/release", releaseRequest{HostID: hostID, TaskID: taskID}, nil)
	return err
}

// PutCredential uploads a credential (e.g. kind "claude_oauth") to the gateway's encrypted
// store (L3, ADR-0049). The gateway seals it at rest. The token is sent ONLY in the request
// body over the authed channel and never logged. A 503 (no master key on the gateway) or 501
// (no credential store) surfaces as an *Error so the caller can report it.
func (c *Client) PutCredential(ctx context.Context, kind, token string) error {
	_, err := c.do(ctx, http.MethodPut, "/agent/credentials/"+kind, putCredentialRequest{Token: token}, nil)
	return err
}

// GetCredential fetches + decrypts a credential from the gateway (L3). found=false (no error)
// means none is stored (the gateway returned 404) — the agent then falls back to its own env /
// an interactive login. The returned token lives only in memory; it is never logged.
func (c *Client) GetCredential(ctx context.Context, kind string) (token string, found bool, err error) {
	var out struct {
		Token string `json:"token"`
	}
	status, err := c.do(ctx, http.MethodGet, "/agent/credentials/"+kind, nil, &out)
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Status == http.StatusNotFound {
			return "", false, nil // none stored — caller falls back
		}
		return "", false, err
	}
	_ = status
	return out.Token, true, nil
}

// DeleteCredential removes a credential from the gateway store (logout propagation). Idempotent
// (deleting an absent kind succeeds on the gateway).
func (c *Client) DeleteCredential(ctx context.Context, kind string) error {
	_, err := c.do(ctx, http.MethodDelete, "/agent/credentials/"+kind, nil, nil)
	return err
}

// TaskDiffBody is the full-file diff the agent stores for a task (Faz-S review parity): the base..
// branch range + the whole-file unified patch the editor reconstructs the native side-by-side diff
// from. The patch is never a secret (it is the change the director reviews); not logged regardless.
type TaskDiffBody struct {
	Base      string
	Branch    string
	Patch     string
	Truncated bool
}

// taskDiffRequest is the JSON body of POST /projects/{id}/agent/tasks/{task}/diff.
type taskDiffRequest struct {
	Base      string `json:"base"`
	Branch    string `json:"branch"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
}

// StoreTaskDiff persists a task's full-file diff so GET /projects/{id}/tasks/{task}/diff serves the
// native side-by-side diff for review. Best-effort at the call site (a store failure is
// observability-only) — but the method propagates errors so the caller can log if it wants.
func (c *Client) StoreTaskDiff(ctx context.Context, projectID, taskID string, body TaskDiffBody) error {
	_, err := c.do(ctx, http.MethodPost, "/projects/"+projectID+"/agent/tasks/"+taskID+"/diff",
		taskDiffRequest(body), nil)
	return err
}

// GetHoldout fetches a holdout body by ref from the gateway (Faz-S S3) so the agent's verify gate
// can inject the ADR-0018 hidden holdout. found=false (no error) means the gateway has no holdout
// for that ref (404) — the gate then runs WITHOUT a holdout (public gates still apply) rather than
// failing the run. File contents arrive base64 (so binary fixtures survive JSON) and are decoded
// here. The holdout body lives only in memory and is never logged.
func (c *Client) GetHoldout(ctx context.Context, ref string) (name string, files map[string][]byte, found bool, err error) {
	var out struct {
		Name  string            `json:"name"`
		Files map[string]string `json:"files"`
	}
	if _, derr := c.do(ctx, http.MethodGet, "/agent/holdout?ref="+url.QueryEscape(ref), nil, &out); derr != nil {
		var e *Error
		if errors.As(derr, &e) && e.Status == http.StatusNotFound {
			return "", nil, false, nil // no holdout for this ref — gate runs without it
		}
		return "", nil, false, derr
	}
	files = make(map[string][]byte, len(out.Files))
	for path, b64 := range out.Files {
		content, decErr := base64.StdEncoding.DecodeString(b64)
		if decErr != nil {
			return "", nil, false, fmt.Errorf("agentclient: holdout file %q is not base64: %w", path, decErr)
		}
		files[path] = content
	}
	return out.Name, files, true, nil
}

// do performs one request: body (if non-nil) is JSON-encoded; on a 2xx, out (if
// non-nil) is JSON-decoded from the body. A non-2xx returns an *Error carrying the
// status + secret-free gateway message. The token is set ONLY on the Authorization
// header and never appears in any returned error.
func (c *Client) do(ctx context.Context, method, path string, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("agentclient: encode %s: %w", path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return 0, fmt.Errorf("agentclient: build %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("agentclient: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return resp.StatusCode, fmt.Errorf("agentclient: decode %s: %w", path, err)
			}
		}
		return resp.StatusCode, nil
	}
	return resp.StatusCode, &Error{Status: resp.StatusCode, Message: errorMessage(data, resp.StatusCode)}
}

// errorMessage extracts the gateway's {error: string} body, falling back to a generic
// status line. It never includes the token (the gateway body is secret-free).
func errorMessage(body []byte, status int) string {
	var env struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err == nil && env.Error != "" {
		return env.Error
	}
	return fmt.Sprintf("request failed with status %d", status)
}
