package engine

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

func isZeroVerdict(v Verdict) bool     { return reflect.DeepEqual(v, Verdict{}) }
func isZeroReview(r ReviewResult) bool { return reflect.DeepEqual(r, ReviewResult{}) }

// fakePerformer is the injected runner used in place of a real `claude -p`
// subprocess: it returns canned stdout fixtures (and an optional error) so the
// deterministic parse/normalize path can be tested without spawning a process.
func fakePerformer(stdout string, err error) runnerFunc {
	return func(_ context.Context, _ []string, _ string, _ []byte, _ []string) ([]byte, error) {
		return []byte(stdout), err
	}
}

func devEngine(run runnerFunc) *CommandEngine {
	return NewCommandEngineWithRunner(RecipeConfig{
		DevelopCmd: []string{"fake-develop"},
		VerifyCmd:  []string{"fake-verify"},
		Timeout:    time.Second,
	}, run)
}

var sampleTask = statestore.Task{ID: "A-3", ProjectID: "conductor", Lane: "engine", Tier: "T2"}
var sampleWS = Workspace{Path: "/tmp/ws", Branch: "conductor/builder/A-3"}

// TestCommandEngine_Develop_ParsesVerdictFromProse: stdout has chatter then a
// JSON Verdict; the exact frozen fields are returned.
func TestCommandEngine_Develop_ParsesVerdictFromProse(t *testing.T) {
	stdout := `Thinking about the task...
Ran go build: ok. Ran go test: ok.
Here is my final verdict:
{"result":"pass","branch":"conductor/builder/A-3","commit_sha":"abc123","checks":[{"name":"go build","result":"pass","evidence":"exit 0"}],"files":["internal/engine/command_engine.go"],"blocked_reason":"","summary":"implemented CommandEngine"}
All done.`
	e := devEngine(fakePerformer(stdout, nil))

	v, err := e.Develop(context.Background(), sampleTask, sampleWS)
	if err != nil {
		t.Fatalf("Develop: unexpected error: %v", err)
	}
	if v.Result != "pass" {
		t.Errorf("Result = %q, want pass", v.Result)
	}
	if v.Branch != "conductor/builder/A-3" {
		t.Errorf("Branch = %q", v.Branch)
	}
	if v.CommitSHA != "abc123" {
		t.Errorf("CommitSHA = %q", v.CommitSHA)
	}
	if len(v.Checks) != 1 || v.Checks[0].Name != "go build" || v.Checks[0].Result != "pass" {
		t.Errorf("Checks = %+v", v.Checks)
	}
	if len(v.Files) != 1 || v.Files[0] != "internal/engine/command_engine.go" {
		t.Errorf("Files = %+v", v.Files)
	}
	if v.Summary != "implemented CommandEngine" {
		t.Errorf("Summary = %q", v.Summary)
	}
}

// TestCommandEngine_Develop_Malformed_ReturnsErrMalformedVerdict covers outputs
// that must NOT yield a Verdict: pure prose, broken JSON, a result-less object,
// and a non-JSON "all tests passed" line (never-fake-green guard).
func TestCommandEngine_Develop_Malformed_ReturnsErrMalformedVerdict(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
	}{
		{"pure prose", "I finished the work and everything looks great."},
		{"all tests passed prose", "RESULT: all tests passed. PASS. Build OK."},
		{"broken json", `here you go: {"result":"pass", "branch": }`},
		{"result-less object", `Done: {"branch":"b","commit_sha":"x","summary":"no result key"}`},
		{"empty result string", `{"result":"","summary":"blank"}`},
		{"non-string result", `{"result": 123, "summary":"numeric"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := devEngine(fakePerformer(tc.stdout, nil))
			v, err := e.Develop(context.Background(), sampleTask, sampleWS)
			if !errors.Is(err, ErrMalformedVerdict) {
				t.Fatalf("err = %v, want ErrMalformedVerdict", err)
			}
			if !isZeroVerdict(v) {
				t.Fatalf("verdict not zero on malformed: %+v", v)
			}
		})
	}
}

// TestCommandEngine_Develop_EmptyOrTimeout_ReturnsErrNoVerdict covers empty
// stdout and a ctx timeout / cancellation from the runner.
func TestCommandEngine_Develop_EmptyOrTimeout_ReturnsErrNoVerdict(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		runErr error
	}{
		{"empty stdout", "", nil},
		{"whitespace only", "   \n\t ", nil},
		{"deadline exceeded", "", context.DeadlineExceeded},
		{"canceled no output", "", context.Canceled},
		{"run error no output", "", errors.New("exit status 1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := devEngine(fakePerformer(tc.stdout, tc.runErr))
			v, err := e.Develop(context.Background(), sampleTask, sampleWS)
			if !errors.Is(err, ErrNoVerdict) {
				t.Fatalf("err = %v, want ErrNoVerdict", err)
			}
			if !isZeroVerdict(v) {
				t.Fatalf("verdict not zero: %+v", v)
			}
		})
	}
}

// TestCommandEngine_Develop_AuthMarker_ReturnsErrAuthExpired: an auth-wall marker
// stops the whole tick regardless of any other content.
func TestCommandEngine_Develop_AuthMarker_ReturnsErrAuthExpired(t *testing.T) {
	cases := []string{
		"Error: Not logged in. Please run /login.",
		"OAuth token expired",
		"Invalid API key provided",
		// auth marker wins even if a JSON verdict is also present.
		`Not logged in {"result":"pass","summary":"should be ignored"}`,
	}
	for _, stdout := range cases {
		t.Run(stdout[:min(len(stdout), 20)], func(t *testing.T) {
			e := devEngine(fakePerformer(stdout, nil))
			v, err := e.Develop(context.Background(), sampleTask, sampleWS)
			if !errors.Is(err, ErrAuthExpired) {
				t.Fatalf("err = %v, want ErrAuthExpired", err)
			}
			if !isZeroVerdict(v) {
				t.Fatalf("verdict not zero on auth wall: %+v", v)
			}
		})
	}
}

// TestCommandEngine_Develop_RateLimit_ReturnsErrRateLimited: a non-zero exit plus a
// usage/rate-limit marker means the performer's account hit its rolling window (a wall
// only time clears) → ErrRateLimited, so the caller backs off instead of blocking the task.
func TestCommandEngine_Develop_RateLimit_ReturnsErrRateLimited(t *testing.T) {
	cases := []string{
		"Claude usage limit reached. Your limit will reset at 3pm.",
		`{"type":"error","error":{"type":"rate_limit_error","message":"..."}}`,
		"You've reached your usage limit — upgrade to increase it.",
		"Approaching your 5-hour limit",
	}
	for _, stdout := range cases {
		t.Run(stdout[:min(len(stdout), 20)], func(t *testing.T) {
			e := devEngine(fakePerformer(stdout, errors.New("exit status 1")))
			v, err := e.Develop(context.Background(), sampleTask, sampleWS)
			if !errors.Is(err, ErrRateLimited) {
				t.Fatalf("err = %v, want ErrRateLimited", err)
			}
			if !isZeroVerdict(v) {
				t.Fatalf("verdict not zero on rate limit: %+v", v)
			}
		})
	}
}

// TestClassifyOutput_RateLimitPhraseButSuccess_NotLimited: a SUCCESSFUL run (nil error)
// that merely NARRATES "usage limit" must NOT be misread as a limit — it parses normally,
// so good committed work is never discarded by a false backoff.
func TestClassifyOutput_RateLimitPhraseButSuccess_NotLimited(t *testing.T) {
	out := []byte(`I reviewed the usage limit reached policy doc. {"result":"pass","summary":"done"}`)
	if err := classifyOutput(out, nil); err != nil {
		t.Fatalf("successful run narrating 'usage limit' misclassified: %v", err)
	}
}

// TestClassifyOutput_AuthBeatsRateLimit: if both an auth wall and a limit phrase are
// present, auth wins (the more fundamental human-gate that time alone cannot clear).
func TestClassifyOutput_AuthBeatsRateLimit(t *testing.T) {
	err := classifyOutput([]byte("Not logged in. usage limit reached"), errors.New("exit 1"))
	if !errors.Is(err, ErrAuthExpired) {
		t.Fatalf("err = %v, want ErrAuthExpired (auth precedence)", err)
	}
}

// TestCommandEngine_Develop_LastValidBlockWins exercises the holdout-style cases:
// multiple result-keyed blocks (the LAST wins), a result-less block before the
// real one (ignored), and JSON inside a markdown fence.
func TestCommandEngine_Develop_LastValidBlockWins(t *testing.T) {
	t.Run("multiple blocks last wins", func(t *testing.T) {
		stdout := `Draft: {"result":"fail","summary":"first attempt","branch":"b1"}
Reconsidered. Final:
{"result":"pass","summary":"final attempt","branch":"b2"}`
		e := devEngine(fakePerformer(stdout, nil))
		v, err := e.Develop(context.Background(), sampleTask, sampleWS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Result != "pass" || v.Summary != "final attempt" || v.Branch != "b2" {
			t.Fatalf("did not select last block: %+v", v)
		}
	})

	t.Run("result-less block then valid", func(t *testing.T) {
		stdout := `metadata: {"branch":"b","files":["x"]}
verdict: {"result":"blocked","blocked_reason":"dep missing","summary":"blocked"}`
		e := devEngine(fakePerformer(stdout, nil))
		v, err := e.Develop(context.Background(), sampleTask, sampleWS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Result != "blocked" || v.BlockedReason != "dep missing" {
			t.Fatalf("wrong block selected: %+v", v)
		}
	})

	t.Run("json in markdown fence", func(t *testing.T) {
		stdout := "Result below:\n```json\n{\"result\":\"pass\",\"summary\":\"fenced\"}\n```\n"
		e := devEngine(fakePerformer(stdout, nil))
		v, err := e.Develop(context.Background(), sampleTask, sampleWS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Result != "pass" || v.Summary != "fenced" {
			t.Fatalf("fenced JSON not parsed: %+v", v)
		}
	})

	t.Run("braces inside strings do not break nesting", func(t *testing.T) {
		stdout := `{"result":"pass","summary":"it printed {not json} and }{ chars","branch":"b"}`
		e := devEngine(fakePerformer(stdout, nil))
		v, err := e.Develop(context.Background(), sampleTask, sampleWS)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if v.Result != "pass" || v.Branch != "b" {
			t.Fatalf("string-brace handling failed: %+v", v)
		}
	})
}

// TestCommandEngine_Verify_ReturnsReviewResult: verify stdout parses into a
// ReviewResult; malformed verify output still yields a sentinel, not fake-green.
func TestCommandEngine_Verify_ReturnsReviewResult(t *testing.T) {
	t.Run("changes requested", func(t *testing.T) {
		stdout := `Reviewing the diff...
{"result":"changes-requested","findings":["missing facade binding","no test for timeout"],"summary":"needs work"}`
		e := devEngine(fakePerformer(stdout, nil))
		r, err := e.Verify(context.Background(), Verdict{Result: "pass"}, sampleWS)
		if err != nil {
			t.Fatalf("Verify: unexpected error: %v", err)
		}
		if r.Result != "changes-requested" {
			t.Errorf("Result = %q", r.Result)
		}
		if len(r.Findings) != 2 {
			t.Errorf("Findings = %+v", r.Findings)
		}
		if r.Summary != "needs work" {
			t.Errorf("Summary = %q", r.Summary)
		}
	})

	t.Run("pass", func(t *testing.T) {
		stdout := `{"result":"pass","findings":[],"summary":"looks good"}`
		e := devEngine(fakePerformer(stdout, nil))
		r, err := e.Verify(context.Background(), Verdict{Result: "pass"}, sampleWS)
		if err != nil {
			t.Fatalf("Verify: unexpected error: %v", err)
		}
		if r.Result != "pass" {
			t.Errorf("Result = %q", r.Result)
		}
	})

	t.Run("malformed verify is not fake-green", func(t *testing.T) {
		e := devEngine(fakePerformer("looks fine to me, approved", nil))
		r, err := e.Verify(context.Background(), Verdict{Result: "pass"}, sampleWS)
		if !errors.Is(err, ErrMalformedVerdict) {
			t.Fatalf("err = %v, want ErrMalformedVerdict", err)
		}
		if !isZeroReview(r) {
			t.Fatalf("review not zero: %+v", r)
		}
	})
}

// TestCommandEngine_NoCommandConfigured guards against silently running with an
// empty recipe.
func TestCommandEngine_NoCommandConfigured(t *testing.T) {
	e := NewCommandEngineWithRunner(RecipeConfig{}, fakePerformer(`{"result":"pass"}`, nil))
	if _, err := e.Develop(context.Background(), sampleTask, sampleWS); !errors.Is(err, ErrNoVerdict) {
		t.Errorf("Develop with no command: err = %v, want ErrNoVerdict", err)
	}
	if _, err := e.Verify(context.Background(), Verdict{}, sampleWS); !errors.Is(err, ErrNoVerdict) {
		t.Errorf("Verify with no command: err = %v, want ErrNoVerdict", err)
	}
}

// TestCommandEngine_Health derives a deterministic signal from observed activity.
func TestCommandEngine_Health(t *testing.T) {
	e := devEngine(fakePerformer(`{"result":"pass","summary":"ok"}`, nil))

	// Before any run, signal is unknown.
	hs, err := e.Health(context.Background(), Session{ID: "s1", TaskID: "A-3"})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if hs.Signal != "unknown" {
		t.Errorf("pre-run Signal = %q, want unknown", hs.Signal)
	}

	if _, err := e.Develop(context.Background(), sampleTask, sampleWS); err != nil {
		t.Fatalf("Develop: %v", err)
	}
	hs, err = e.Health(context.Background(), Session{ID: "s1", TaskID: "A-3"})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if hs.Signal != "progressing" {
		t.Errorf("post-run Signal = %q, want progressing", hs.Signal)
	}
	if hs.LastActivityTS.IsZero() {
		t.Errorf("LastActivityTS not set")
	}
}

func TestCommandEngine_HealthRespectsCtx(t *testing.T) {
	e := devEngine(fakePerformer("", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Health(ctx, Session{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Health cancelled ctx: err = %v", err)
	}
}

// TestCommandEngine_Control accepts the three actions and rejects others.
func TestCommandEngine_Control(t *testing.T) {
	e := devEngine(fakePerformer("", nil))
	for _, action := range []string{"pause", "resume", "abort"} {
		if err := e.Control(context.Background(), Command{Action: action}); err != nil {
			t.Errorf("Control(%q): %v", action, err)
		}
	}
	if err := e.Control(context.Background(), Command{Action: "explode"}); err == nil {
		t.Error("Control(explode): want error for unknown action")
	}
}

// TestCommandEngine_Events emits normalized envelopes and closes on ctx cancel.
func TestCommandEngine_Events(t *testing.T) {
	stdout := `{"phase":"develop","kind":"started","payload":{"task":"A-3"}}
some prose that is not an event
{"result":"pass","summary":"ok"}
{"phase":"test","kind":"progress","payload":{"pct":50}}`
	e := devEngine(fakePerformer(stdout, nil))
	if _, err := e.Develop(context.Background(), sampleTask, sampleWS); err != nil {
		t.Fatalf("Develop: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := e.Events(ctx)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var got []Event
	// Two structured event lines were emitted (the verdict line has no
	// phase/kind, so it is not an event).
	for i := 0; i < 2; i++ {
		select {
		case ev := <-ch:
			got = append(got, ev)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
	if got[0].Phase != "develop" || got[0].Kind != "started" {
		t.Errorf("event[0] = %+v", got[0])
	}
	if got[1].Phase != "test" || got[1].Kind != "progress" {
		t.Errorf("event[1] = %+v", got[1])
	}

	cancel()
	select {
	case _, open := <-ch:
		if open {
			// drain a possible in-flight value, then confirm closure.
			if _, open2 := <-ch; open2 {
				t.Error("channel did not close after ctx cancel")
			}
		}
	case <-time.After(time.Second):
		t.Error("channel not closed after ctx cancel")
	}
}

func TestCommandEngine_EventsNilCtx(t *testing.T) {
	e := devEngine(fakePerformer("", nil))
	if _, err := e.Events(nil); err == nil { //nolint:staticcheck // deliberately passing nil ctx to test guard.
		t.Error("Events(nil): want error")
	}
}

// TestCommandEngine_DevelopRunsInWorkspace confirms the workspace path and stdin
// payload reach the runner.
func TestCommandEngine_DevelopRunsInWorkspace(t *testing.T) {
	var gotDir string
	var gotStdin []byte
	var gotArgv []string
	run := func(_ context.Context, argv []string, dir string, stdin []byte, _ []string) ([]byte, error) {
		gotArgv = argv
		gotDir = dir
		gotStdin = stdin
		return []byte(`{"result":"pass","summary":"ok"}`), nil
	}
	e := devEngine(run)
	if _, err := e.Develop(context.Background(), sampleTask, sampleWS); err != nil {
		t.Fatalf("Develop: %v", err)
	}
	if gotDir != sampleWS.Path {
		t.Errorf("dir = %q, want %q", gotDir, sampleWS.Path)
	}
	if len(gotArgv) == 0 || gotArgv[0] != "fake-develop" {
		t.Errorf("argv = %v", gotArgv)
	}
	if !containsAll(string(gotStdin), "A-3", sampleWS.Path) {
		t.Errorf("stdin missing task/workspace context: %s", gotStdin)
	}
}

// TestExecRunner_StripsSecretEnvFromPerformer is the S-1 proof at the engine
// level through the REAL execRunner path (NewCommandEngine, not a fake runner):
// the develop performer is `sh -c 'env; {verdict}'` which dumps its own
// environment to stdout. With GH_TOKEN + CONDUCTOR_DSN + CLAUDE_CODE_OAUTH_TOKEN
// set in the parent, the performer's env must NOT contain the daemon's secrets
// (GH_TOKEN/CONDUCTOR_DSN) but MUST still carry the Claude OAuth token and PATH
// (denylist, not aggressive strip). We read the dumped env straight off the
// performer stdout, so this asserts the actual cmd.Env the subprocess received.
func TestExecRunner_StripsSecretEnvFromPerformer(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	t.Setenv("GH_TOKEN", "ghp_faketokenvalue")
	t.Setenv("CONDUCTOR_DSN", "postgres://leak:leak@host/db")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "claude-oauth-fake")

	// The performer dumps its env, then emits a valid Verdict so Develop returns
	// cleanly and we can capture the env via the engine's normal path.
	dump := `env; echo '{"result":"pass","summary":"ok"}'`
	e := NewCommandEngine(RecipeConfig{
		DevelopCmd: []string{"sh", "-c", dump},
		Timeout:    10 * time.Second,
	})

	// Capture the performer's combined output (which begins with its env dump) by
	// running the real runner directly — same code path NewCommandEngine uses.
	out, err := execRunner(context.Background(), []string{"sh", "-c", dump}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("execRunner: %v", err)
	}
	env := string(out)

	for _, secret := range []string{"GH_TOKEN=", "CONDUCTOR_DSN="} {
		if strings.Contains(env, secret) {
			t.Errorf("performer env leaked daemon secret %q (S-1 not fixed)", secret)
		}
	}
	if !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=claude-oauth-fake") {
		t.Errorf("Claude OAuth token was stripped — performer would break (over-sanitized)")
	}
	if !strings.Contains(env, "PATH=") {
		t.Errorf("PATH was stripped — toolchain would break (over-sanitized)")
	}

	// And the engine end-to-end still parses the verdict cleanly via the same path.
	v, err := e.Develop(context.Background(), sampleTask, Workspace{Path: t.TempDir(), Branch: "b"})
	if err != nil {
		t.Fatalf("Develop via real execRunner: %v", err)
	}
	if v.Result != "pass" {
		t.Fatalf("verdict = %+v, want pass", v)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
