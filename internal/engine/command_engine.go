package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// RecipeConfig is the per-project recipe binding the CommandEngine drives
// (ADR-0009, ADR-0014). The platform reads these from the project's .conductor
// recipe and hands them to the constructor; there is no global singleton.
//
// DevelopCmd and VerifyCmd are argv slices (program + args) so they are executed
// directly via exec.CommandContext without a shell — the in-performer pipeline
// runs inside that subprocess (typically `claude -p`), not in Go (ADR-0002).
type RecipeConfig struct {
	// DevelopCmd is the argv of the recipe develop command (program + args).
	DevelopCmd []string
	// VerifyCmd is the argv of the recipe verify command (program + args).
	VerifyCmd []string
	// Timeout bounds each subprocess; a zero or negative value disables the
	// engine-level deadline and relies on the caller's ctx instead.
	Timeout time.Duration
	// Env is extra environment passed to the subprocess on top of the parent's;
	// each entry is "KEY=VALUE".
	Env []string
}

// runnerFunc executes a single performer invocation and returns its combined
// stdout (the channel the Verdict/ReviewResult JSON arrives on). It is the seam
// that makes the subprocess injectable: production uses execRunner, tests pass a
// fake performer that returns fixture stdout without spawning `claude -p`.
//
// argv is the resolved command (RecipeConfig.DevelopCmd or VerifyCmd), dir is
// the workspace path it runs in, stdin is the contract payload fed to the
// performer, and env is the merged environment. The returned error is the raw
// process error (e.g. context.DeadlineExceeded, *exec.ExitError); the engine
// classifies it into a sentinel.
type runnerFunc func(ctx context.Context, argv []string, dir string, stdin []byte, env []string) (stdout []byte, err error)

// CommandEngine is the single generic EngineAdapter (ADR-0002): it runs recipe
// commands as subprocesses and deterministically normalizes their stdout into a
// Verdict or ReviewResult (ADR-0014). It holds no LLM logic itself — the
// development pipeline lives inside the performer process. It is safe for
// concurrent use; the only mutable state is the small control/health bookkeeping
// guarded by a mutex.
type CommandEngine struct {
	recipe RecipeConfig
	run    runnerFunc

	mu           sync.Mutex
	lastActivity time.Time
	lastPhase    string
	paused       bool
	aborted      bool
	events       []Event
}

// Compile-time assertion that *CommandEngine satisfies the frozen contract.
var _ EngineAdapter = (*CommandEngine)(nil)

// NewCommandEngine returns a CommandEngine that drives the given recipe with a
// real subprocess runner (exec.CommandContext). Use NewCommandEngineWithRunner
// to inject a fake performer in tests.
func NewCommandEngine(recipe RecipeConfig) *CommandEngine {
	return NewCommandEngineWithRunner(recipe, execRunner)
}

// NewCommandEngineWithRunner returns a CommandEngine whose subprocess execution
// is supplied by run. It is the test seam: a fake performer can return fixture
// stdout (valid Verdict in prose, malformed, empty, auth-marker) without any
// real `claude -p` invocation. A nil runner falls back to the real execRunner.
func NewCommandEngineWithRunner(recipe RecipeConfig, run runnerFunc) *CommandEngine {
	if run == nil {
		run = execRunner
	}
	return &CommandEngine{recipe: recipe, run: run}
}

// Develop runs the recipe develop command in ws.Path with the contract payload
// on stdin (scenario / public-tests / rules / workspace, ADR-0014), enforces the
// recipe timeout via ctx, and returns the parsed Verdict.
//
// It NEVER fakes green: on a parse failure, empty/timed-out output, or an
// auth-wall marker it returns a zero Verdict and a wrapped sentinel
// (ErrMalformedVerdict / ErrNoVerdict / ErrAuthExpired) that callers detect with
// errors.Is. Self-heal/retry is the performer's job inside the subprocess.
func (e *CommandEngine) Develop(ctx context.Context, task statestore.Task, ws Workspace) (Verdict, error) {
	if len(e.recipe.DevelopCmd) == 0 {
		return Verdict{}, fmt.Errorf("engine: develop: %w: no develop command configured", ErrNoVerdict)
	}
	stdin := developStdin(task, ws)
	stdout, runErr := e.invoke(ctx, e.recipe.DevelopCmd, ws.Path, stdin)
	if err := classifyOutput(stdout, runErr); err != nil {
		return Verdict{}, fmt.Errorf("engine: develop %q: %w", task.ID, err)
	}
	v, err := ParseVerdict(stdout)
	if err != nil {
		return Verdict{}, fmt.Errorf("engine: develop %q: %w", task.ID, err)
	}
	return v, nil
}

// Verify runs the recipe verify command over a prior Verdict's evidence and
// returns a ReviewResult (ADR-0003). It reviews; it does not develop. It applies
// the same resilience classification and never fabricates a passing review.
func (e *CommandEngine) Verify(ctx context.Context, verdict Verdict, ws Workspace) (ReviewResult, error) {
	if len(e.recipe.VerifyCmd) == 0 {
		return ReviewResult{}, fmt.Errorf("engine: verify: %w: no verify command configured", ErrNoVerdict)
	}
	stdin := verifyStdin(verdict, ws)
	stdout, runErr := e.invoke(ctx, e.recipe.VerifyCmd, ws.Path, stdin)
	if err := classifyOutput(stdout, runErr); err != nil {
		return ReviewResult{}, fmt.Errorf("engine: verify: %w", err)
	}
	r, err := ParseReviewResult(stdout)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("engine: verify: %w", err)
	}
	return r, nil
}

// invoke applies the recipe timeout, records activity for Health, and delegates
// to the injected runner.
func (e *CommandEngine) invoke(ctx context.Context, argv []string, dir string, stdin []byte) ([]byte, error) {
	if e.recipe.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.recipe.Timeout)
		defer cancel()
	}
	e.markActivity("develop")
	stdout, err := e.run(ctx, argv, dir, stdin, e.recipe.Env)
	e.markActivity("develop")
	e.ingestEvents(stdout)
	return stdout, err
}

// markActivity records the last time output flowed and the phase, feeding the
// deterministic Health signal (ADR-0006).
func (e *CommandEngine) markActivity(phase string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastActivity = time.Now()
	e.lastPhase = phase
}

// ingestEvents normalizes any structured event lines in subprocess output and
// buffers them for delivery via Events (ADR-0011).
func (e *CommandEngine) ingestEvents(stdout []byte) {
	var batch []Event
	for _, line := range scanLines(stdout) {
		if ev, ok := normalizeEventLine("", "", line); ok {
			batch = append(batch, ev)
		}
	}
	if len(batch) == 0 {
		return
	}
	e.mu.Lock()
	e.events = append(e.events, batch...)
	e.mu.Unlock()
}

// Health derives a deterministic Layer-1 liveness signal from observed
// subprocess activity (ADR-0006): how long ago output last flowed maps to
// progressing / idle / unknown. It performs no probing of its own — it reports
// what the engine has observed.
func (e *CommandEngine) Health(ctx context.Context, _ Session) (HealthState, error) {
	if err := ctx.Err(); err != nil {
		return HealthState{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.lastActivity.IsZero() {
		return HealthState{Signal: "unknown"}, nil
	}
	signal := "progressing"
	if time.Since(e.lastActivity) > healthIdleThreshold {
		signal = "idle"
	}
	return HealthState{
		Phase:          e.lastPhase,
		LastActivityTS: e.lastActivity,
		Signal:         signal,
	}, nil
}

// healthIdleThreshold is how long without observed output before a session is
// reported idle rather than progressing.
const healthIdleThreshold = 90 * time.Second

// Events returns a channel of normalized Event envelopes (ADR-0011). The engine
// only normalizes and forwards; persisting and NOTIFY-ing is the platform's job.
// The returned channel is closed when ctx is cancelled so consumers drain
// cleanly. A nil context returns an error rather than leaking a goroutine.
func (e *CommandEngine) Events(ctx context.Context) (<-chan Event, error) {
	if ctx == nil {
		return nil, errors.New("engine: events: nil context")
	}
	e.mu.Lock()
	buffered := append([]Event(nil), e.events...)
	e.mu.Unlock()

	out := make(chan Event)
	go func() {
		defer close(out)
		for _, ev := range buffered {
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return out, nil
}

// Control delivers a pause/resume/abort directive to the engine (ADR-0002). The
// action is recorded so a running invocation's lifecycle reflects it; an unknown
// action is rejected rather than silently ignored.
func (e *CommandEngine) Control(ctx context.Context, cmd Command) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	switch cmd.Action {
	case ActionPause:
		e.paused = true
	case ActionResume:
		e.paused = false
	case ActionAbort:
		e.aborted = true
	default:
		return fmt.Errorf("engine: control: unknown action %q", cmd.Action)
	}
	return nil
}

// classifyOutput maps a raw runner outcome onto the resilience sentinels before
// any JSON parsing is attempted (ADR-0014). It is the never-fake-green gate:
//
//   - an auth-wall marker anywhere in the output -> ErrAuthExpired (the whole
//     tick must stop; retrying would just hit the wall again);
//   - a timeout/cancellation or empty output -> ErrNoVerdict;
//   - any other run error with no usable output -> ErrNoVerdict.
//
// A nil return means the output is worth handing to the parser.
func classifyOutput(stdout []byte, runErr error) error {
	if marker := detectAuthMarker(stdout); marker != "" {
		return fmt.Errorf("%w: %s", ErrAuthExpired, marker)
	}
	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, context.Canceled) {
			return fmt.Errorf("%w: %v", ErrNoVerdict, runErr)
		}
		// A process error with usable output (e.g. exit 1 but a JSON verdict on
		// stdout) is still parseable; fall through. Otherwise it is a no-verdict.
		if len(bytes.TrimSpace(stdout)) == 0 {
			return fmt.Errorf("%w: %v", ErrNoVerdict, runErr)
		}
	}
	if len(bytes.TrimSpace(stdout)) == 0 {
		return fmt.Errorf("%w: empty output", ErrNoVerdict)
	}
	return nil
}

// authMarkers are the auth-wall signatures that mean the performer never got to
// run (ADR-0014). They are matched case-insensitively.
var authMarkers = []string{
	"not logged in",
	"oauth token expired",
	"invalid api key",
	"please run /login",
	"authentication_error",
}

// detectAuthMarker returns the first auth-wall marker found in out, or "".
func detectAuthMarker(out []byte) string {
	lower := strings.ToLower(string(out))
	for _, m := range authMarkers {
		if strings.Contains(lower, m) {
			return m
		}
	}
	return ""
}

// ParseVerdict extracts a Verdict from performer stdout that may be wrapped in
// prose or markdown (ADR-0014). It is the deterministic LLM-resilience parser:
// it scans for the LAST balanced JSON object that carries a "result" key and
// decodes it into a Verdict. Anything else (no JSON, only result-less objects,
// non-JSON "all tests passed" prose) is a parse failure that wraps
// ErrMalformedVerdict — it is NEVER coerced into a passing Verdict.
func ParseVerdict(stdout []byte) (Verdict, error) {
	obj, err := lastResultObject(stdout)
	if err != nil {
		return Verdict{}, err
	}
	var v Verdict
	if err := json.Unmarshal(obj, &v); err != nil {
		return Verdict{}, fmt.Errorf("%w: decode verdict: %v", ErrMalformedVerdict, err)
	}
	if v.Result == "" {
		return Verdict{}, fmt.Errorf("%w: verdict missing result", ErrMalformedVerdict)
	}
	return v, nil
}

// ParseReviewResult extracts a ReviewResult from verify stdout using the same
// last-valid-result-object selection as ParseVerdict.
func ParseReviewResult(stdout []byte) (ReviewResult, error) {
	obj, err := lastResultObject(stdout)
	if err != nil {
		return ReviewResult{}, err
	}
	var r ReviewResult
	if err := json.Unmarshal(obj, &r); err != nil {
		return ReviewResult{}, fmt.Errorf("%w: decode review: %v", ErrMalformedVerdict, err)
	}
	if r.Result == "" {
		return ReviewResult{}, fmt.Errorf("%w: review missing result", ErrMalformedVerdict)
	}
	return r, nil
}

// lastResultObject returns the raw bytes of the LAST top-level JSON object in
// data that both parses as JSON and contains a non-empty "result" string key. It
// scans every balanced {...} span (skipping braces inside JSON strings), so it
// is robust to prose, markdown fences, and multiple candidate blocks. The
// last-valid-block rule lets a performer narrate, emit a draft, then emit the
// final verdict last (ADR-0014, ADR-0018).
//
// It returns a wrapped ErrMalformedVerdict if no qualifying object exists.
func lastResultObject(data []byte) ([]byte, error) {
	var last []byte
	for _, span := range balancedObjects(data) {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(span, &probe); err != nil {
			continue // not a valid JSON object
		}
		raw, ok := probe["result"]
		if !ok {
			continue // result-less object is not a Verdict/ReviewResult
		}
		var result string
		if err := json.Unmarshal(raw, &result); err != nil || result == "" {
			continue // result must be a non-empty string
		}
		last = span
	}
	if last == nil {
		return nil, fmt.Errorf("%w: no result-keyed JSON object in output", ErrMalformedVerdict)
	}
	return last, nil
}

// balancedObjects returns every balanced top-level {...} byte span in data, in
// order of appearance, tracking string and escape state so that braces inside
// JSON string literals do not corrupt nesting. Nested objects are folded into
// their enclosing top-level span (only the outermost object is emitted), which
// is exactly the granularity a Verdict/ReviewResult needs.
func balancedObjects(data []byte) [][]byte {
	var spans [][]byte
	depth := 0
	start := -1
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					spans = append(spans, data[start:i+1])
					start = -1
				}
			}
		}
	}
	return spans
}

// developStdin builds the contract payload fed to the develop performer on
// stdin (ADR-0014): the task identity and the workspace it must operate in. The
// scenario/public-tests/rules pointers travel via the task and workspace; the
// performer reads them from the checkout. It is plain JSON so the recipe command
// can consume it without a bespoke protocol.
func developStdin(task statestore.Task, ws Workspace) []byte {
	payload := map[string]any{
		"task": map[string]any{
			"id":          task.ID,
			"project_id":  task.ProjectID,
			"lane":        task.Lane,
			"tier":        task.Tier,
			"scenario_id": task.ScenarioID,
			"branch":      task.Branch,
			"deps":        task.Deps,
			"requires":    task.Requires,
		},
		"workspace": map[string]any{
			"path":   ws.Path,
			"branch": ws.Branch,
		},
	}
	return mustJSON(payload)
}

// verifyStdin builds the contract payload fed to the verify performer on stdin:
// the prior Verdict to review and the workspace its evidence lives in.
func verifyStdin(verdict Verdict, ws Workspace) []byte {
	payload := map[string]any{
		"verdict": verdict,
		"workspace": map[string]any{
			"path":   ws.Path,
			"branch": ws.Branch,
		},
	}
	return mustJSON(payload)
}

// mustJSON marshals v, falling back to an empty object on the impossible error
// path so stdin construction never panics or fails the call.
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// execRunner is the production runnerFunc: it runs argv via exec.CommandContext
// in dir with the parent environment plus env, feeds stdin, and returns combined
// stdout+stderr (the performer narrates on both, and auth/JSON markers can land
// on either). The process is started in its own group so Control can signal the
// whole performer tree, not just the leader.
func execRunner(ctx context.Context, argv []string, dir string, stdin []byte, env []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("engine: empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is the operator-supplied recipe command.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = bytes.NewReader(stdin)
	configureProcessGroup(cmd)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// normalizeEventLine parses a single subprocess output line into a normalized
// Event if it is a structured event envelope, returning ok=false for plain prose
// (ADR-0011). It is used by Events-style consumers to turn performer chatter into
// observability envelopes without inventing data.
func normalizeEventLine(project, task string, line []byte) (Event, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Event{}, false
	}
	var env struct {
		Phase   string         `json:"phase"`
		Kind    string         `json:"kind"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(trimmed, &env); err != nil {
		return Event{}, false
	}
	if env.Phase == "" && env.Kind == "" {
		return Event{}, false
	}
	return Event{
		TS:      time.Now(),
		Project: project,
		Task:    task,
		Phase:   env.Phase,
		Kind:    env.Kind,
		Payload: env.Payload,
	}, true
}

// scanLines splits subprocess output into lines for event normalization. It is a
// thin wrapper kept separate so callers do not duplicate bufio wiring.
func scanLines(data []byte) [][]byte {
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	return lines
}
