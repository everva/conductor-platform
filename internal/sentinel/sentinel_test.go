package sentinel

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubAdvisor is a deterministic Advisor for the offline gate: it returns a fixed
// advice/reason/error and records whether it was consulted, so a test can assert
// BOTH the verdict mapping AND the precedence guarantee that the advisor is NOT
// called when a deterministic layer already decides (Layer-1 fresh / Layer-3
// backstop). No `claude -p` is ever spawned.
type stubAdvisor struct {
	advice Advice
	reason string
	err    error
	calls  int
}

func (s *stubAdvisor) Advise(_ context.Context, _ string) (Advice, string, error) {
	s.calls++
	return s.advice, s.reason, s.err
}

// cfg is a compact test config: small thresholds so the durations in Signals read
// clearly. Fresh < 1s, gray zone from 2s, backstop at 10s.
func cfg() Config {
	return Config{
		MaxTotal:      10 * time.Second,
		FreshActivity: 1 * time.Second,
		GraceUnsure:   2 * time.Second,
	}
}

// TestLayer1_FreshActivity_Continue_AdvisorNotCalled proves the cheap common
// case: fresh activity returns Continue at Layer 1 WITHOUT consulting the advisor
// (no LLM load on healthy runs).
func TestLayer1_FreshActivity_Continue_AdvisorNotCalled(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceStuck} // would Kill if (wrongly) consulted
	s := New(cfg(), adv)

	res := s.Assess(context.Background(), Signals{
		Elapsed:       3 * time.Second,
		SinceActivity: 100 * time.Millisecond, // fresh
		Alive:         true,
	})

	if res.Decision != Continue {
		t.Fatalf("fresh activity: decision = %q, want %q", res.Decision, Continue)
	}
	if res.Layer != 1 {
		t.Fatalf("fresh activity: layer = %d, want 1", res.Layer)
	}
	if adv.calls != 0 {
		t.Fatalf("fresh activity MUST NOT consult the advisor, got %d calls", adv.calls)
	}
}

// TestLayer1_ClearlyDead_Kill proves a stalled run whose process is not alive is
// killed deterministically at Layer 1 without the advisor.
func TestLayer1_ClearlyDead_Kill(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceProgressing} // would Continue if consulted
	s := New(cfg(), adv)

	res := s.Assess(context.Background(), Signals{
		Elapsed:       3 * time.Second,
		SinceActivity: 5 * time.Second, // stalled past grace
		Alive:         false,           // clearly dead
	})

	if res.Decision != Kill || res.Layer != 1 {
		t.Fatalf("clearly dead: got decision=%q layer=%d, want Kill@1", res.Decision, res.Layer)
	}
	if adv.calls != 0 {
		t.Fatalf("clearly-dead Kill is deterministic; advisor must not be consulted, got %d", adv.calls)
	}
}

// TestLayer2_GrayStuck_Kill proves an alive-but-stalled run whose advisor says
// "stuck" is Killed at Layer 2 (the advisor was consulted exactly once).
func TestLayer2_GrayStuck_Kill(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceStuck, reason: "infinite retry loop"}
	s := New(cfg(), adv)

	res := s.Assess(context.Background(), Signals{
		Elapsed:       3 * time.Second,
		SinceActivity: 5 * time.Second, // stalled past grace
		Alive:         true,            // gray zone
		LastOutput:    "retrying... retrying... retrying...",
	})

	if res.Decision != Kill || res.Layer != 2 {
		t.Fatalf("gray stuck: got decision=%q layer=%d, want Kill@2", res.Decision, res.Layer)
	}
	if res.Advice != AdviceStuck {
		t.Fatalf("gray stuck: advice = %q, want %q", res.Advice, AdviceStuck)
	}
	if adv.calls != 1 {
		t.Fatalf("gray zone must consult the advisor once, got %d", adv.calls)
	}
}

// TestLayer2_GrayNeedsHuman_Escalate proves the needs_human verdict maps to
// Escalate at Layer 2.
func TestLayer2_GrayNeedsHuman_Escalate(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceNeedsHuman, reason: "waiting on a login prompt"}
	s := New(cfg(), adv)

	res := s.Assess(context.Background(), Signals{
		Elapsed:       3 * time.Second,
		SinceActivity: 5 * time.Second,
		Alive:         true,
		LastOutput:    "Please run /login to continue",
	})

	if res.Decision != Escalate || res.Layer != 2 {
		t.Fatalf("gray needs_human: got decision=%q layer=%d, want Escalate@2", res.Decision, res.Layer)
	}
	if res.Advice != AdviceNeedsHuman {
		t.Fatalf("gray needs_human: advice = %q, want %q", res.Advice, AdviceNeedsHuman)
	}
}

// TestLayer2_GrayProgressing_Continue proves the progressing verdict maps to
// Continue at Layer 2.
func TestLayer2_GrayProgressing_Continue(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceProgressing, reason: "still compiling"}
	s := New(cfg(), adv)

	res := s.Assess(context.Background(), Signals{
		Elapsed:       3 * time.Second,
		SinceActivity: 5 * time.Second,
		Alive:         true,
	})

	if res.Decision != Continue || res.Layer != 2 {
		t.Fatalf("gray progressing: got decision=%q layer=%d, want Continue@2", res.Decision, res.Layer)
	}
}

// TestLayer3_BackstopAlwaysWins_AdvisorProgressing_StillKills is THE key
// DF-difference test: the stub advisor says "progressing" (which would otherwise
// Continue) but elapsed has reached MaxTotal — the Layer-3 backstop, checked
// FIRST, Kills the run anyway, and the advisor is NEVER consulted. The LLM
// structurally cannot make the run wait past the ceiling.
func TestLayer3_BackstopAlwaysWins_AdvisorProgressing_StillKills(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceProgressing, reason: "I promise it is still working"}
	s := New(cfg(), adv)

	res := s.Assess(context.Background(), Signals{
		Elapsed:       10 * time.Second, // == MaxTotal: backstop binds
		SinceActivity: 5 * time.Second,  // would be the gray zone
		Alive:         true,             // alive: the advisor WOULD say progressing
		LastOutput:    "working hard, definitely making progress",
	})

	if res.Decision != Kill {
		t.Fatalf("BACKSTOP: decision = %q, want %q (advisor must NOT override the ceiling)", res.Decision, Kill)
	}
	if res.Layer != 3 {
		t.Fatalf("BACKSTOP: layer = %d, want 3", res.Layer)
	}
	if adv.calls != 0 {
		t.Fatalf("BACKSTOP is checked FIRST: the advisor must NEVER be consulted past the ceiling, got %d calls", adv.calls)
	}
}

// TestLayer3_BackstopBeyondCeiling_Kills confirms elapsed strictly past MaxTotal
// also kills (>=, not just ==).
func TestLayer3_BackstopBeyondCeiling_Kills(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceProgressing}
	s := New(cfg(), adv)
	res := s.Assess(context.Background(), Signals{Elapsed: 30 * time.Second, SinceActivity: 5 * time.Second, Alive: true})
	if res.Decision != Kill || res.Layer != 3 {
		t.Fatalf("beyond ceiling: got %q@%d, want Kill@3", res.Decision, res.Layer)
	}
}

// TestNoAdvisor_GrayZone_ContinuesUntilBackstop proves the conservative fallback:
// with NO advisor, an alive-but-stalled gray-zone run Continues (Layer 2, no
// spurious escalate) — and the same sentinel later Kills via the backstop once the
// ceiling is reached.
func TestNoAdvisor_GrayZone_ContinuesUntilBackstop(t *testing.T) {
	s := New(cfg(), nil) // no advisor: Layers 1+3 only

	// Gray zone, no advisor -> conservative Continue.
	res := s.Assess(context.Background(), Signals{Elapsed: 3 * time.Second, SinceActivity: 5 * time.Second, Alive: true})
	if res.Decision != Continue || res.Layer != 2 {
		t.Fatalf("no-advisor gray: got %q@%d, want Continue@2", res.Decision, res.Layer)
	}

	// Same conditions but past the ceiling -> backstop Kills.
	res = s.Assess(context.Background(), Signals{Elapsed: 10 * time.Second, SinceActivity: 5 * time.Second, Alive: true})
	if res.Decision != Kill || res.Layer != 3 {
		t.Fatalf("no-advisor backstop: got %q@%d, want Kill@3", res.Decision, res.Layer)
	}
}

// TestAdvisorError_ConservativeContinue proves an advisor error NEVER spuriously
// escalates or kills: the gray zone falls back to Continue (the backstop still
// bounds the run).
func TestAdvisorError_ConservativeContinue(t *testing.T) {
	adv := &stubAdvisor{err: errors.New("claude unavailable")}
	s := New(cfg(), adv)

	res := s.Assess(context.Background(), Signals{Elapsed: 3 * time.Second, SinceActivity: 5 * time.Second, Alive: true})
	if res.Decision != Continue || res.Layer != 2 {
		t.Fatalf("advisor error: got %q@%d, want Continue@2 (conservative)", res.Decision, res.Layer)
	}
	if adv.calls != 1 {
		t.Fatalf("advisor error path must have actually consulted the advisor once, got %d", adv.calls)
	}
}

// TestGrace_StalledButWithinGrace_Continue proves a stall between FreshActivity
// and GraceUnsure is treated as Continue (Layer 1 grace) WITHOUT spending an
// advisor call.
func TestGrace_StalledButWithinGrace_Continue(t *testing.T) {
	adv := &stubAdvisor{advice: AdviceStuck}
	s := New(cfg(), adv)

	// SinceActivity 1.5s: past FreshActivity(1s) but below GraceUnsure(2s).
	res := s.Assess(context.Background(), Signals{Elapsed: 3 * time.Second, SinceActivity: 1500 * time.Millisecond, Alive: true})
	if res.Decision != Continue || res.Layer != 1 {
		t.Fatalf("within grace: got %q@%d, want Continue@1", res.Decision, res.Layer)
	}
	if adv.calls != 0 {
		t.Fatalf("within grace must not consult the advisor, got %d", adv.calls)
	}
}

// TestConfigDefaults_DisabledBackstop proves a non-positive MaxTotal disables the
// backstop (Layer 3 never fires) while the tuning fields default. With the
// backstop off and no advisor, an alive stall Continues indefinitely (the caller's
// own timeout bounds it).
func TestConfigDefaults_DisabledBackstop(t *testing.T) {
	s := New(Config{}, nil) // MaxTotal 0 -> backstop disabled; defaults fill the rest

	res := s.Assess(context.Background(), Signals{Elapsed: 24 * time.Hour, SinceActivity: 10 * time.Minute, Alive: true})
	if res.Decision != Continue {
		t.Fatalf("disabled backstop: decision = %q, want Continue", res.Decision)
	}
	if res.Layer == 3 {
		t.Fatalf("disabled backstop must never decide at Layer 3")
	}
}
