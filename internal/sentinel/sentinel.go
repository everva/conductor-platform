// Package sentinel is the 3-layer liveness sentinel of ADR-0006 (Faz-2 2C-1): a
// deterministic base, a gray-zone LLM ADVISOR, and a deterministic backstop that
// ALWAYS overrides. The whole point of the design is the DF-difference: the LLM
// is an ADVISOR on liveness, never the decision authority — the deterministic
// backstop binds even when the advisor says "keep waiting", so the LLM can never
// stall the system past the absolute ceiling the way DF's merge-gate LLM did.
//
// The three layers and the precedence between them:
//
//	Layer-3 (backstop, deterministic, checked FIRST): elapsed >= MaxTotal -> Kill.
//	  This is checked before anything else so a "progressing" advisor verdict can
//	  NEVER push the run past the absolute ceiling. The backstop structurally wins
//	  because it is evaluated before the advisor is ever consulted.
//	Layer-1 (deterministic base): fresh activity -> Continue WITHOUT calling the
//	  advisor (the cheap, common case — no LLM load); NOT-alive AND stalled ->
//	  Kill. NOTE on the "clearly dead -> Kill" path: it fires only when a probe can
//	  report Alive=false. The DEFAULT production probe (engineProgressProbe) derives
//	  liveness from observed OUTPUT only and so cannot prove a process dead — it
//	  reports a stalled "idle" run as alive-but-stalled, deferring it to the gray
//	  zone + the Layer-3 backstop rather than killing it at Layer-1. The Layer-1
//	  kill therefore activates with a richer probe that observes real process
//	  liveness; the backstop remains the authoritative bound in the default setup.
//	  Only an alive-but-stalled run reaches Layer-2.
//	Layer-2 (gray zone, LLM advisor, RARE): alive (process up) but output stalled
//	  for >= GraceUnsure -> consult the Advisor. progressing -> Continue, stuck ->
//	  Kill, needs_human -> Escalate. No advisor, or an advisor error, falls back to
//	  the CONSERVATIVE Continue (until Layer-3 backstop) — it NEVER spuriously
//	  escalates or kills on advisor trouble.
//
// The package holds NO LLM logic of its own: the advisor is an injectable seam
// (Advisor), and the only concrete advisor (CommandAdvisor) runs `claude -p` via
// a runner func mirroring engine.runnerFunc, behind the `realclaude` build tag +
// CP_REAL_CLAUDE=1 env. The default tests use a deterministic stub advisor, so
// the gate stays offline. No secret is read or written here.
package sentinel

import (
	"context"
	"time"
)

// Decision is the sentinel's deterministic action on a run. It is the OUTPUT of
// Assess; a caller (the conductor watchdog) maps it onto concrete lifecycle
// actions (cancel develop, escalate to a human, keep going).
type Decision string

const (
	// Continue means let the run proceed. It is the conservative default: a fresh
	// run, an advisor that says progressing, and any advisor trouble all land here
	// — the run is only stopped by an explicit stuck/needs_human verdict or the
	// Layer-3 backstop.
	Continue Decision = "continue"
	// Escalate means a human must intervene: the advisor judged the stall to need a
	// human (needs_human). The caller cancels the run and surfaces an
	// intervention-needed signal rather than silently killing or silently waiting.
	Escalate Decision = "escalate"
	// Kill means stop the run now. It is produced by the Layer-3 backstop (absolute
	// ceiling reached), a clearly-dead Layer-1 signal, or a Layer-2 "stuck" advice.
	Kill Decision = "kill"
)

// Advice is the narrow 3-valued liveness verdict the gray-zone advisor returns
// (ADR-0006 Katman-2). It is deliberately an enum with no score: no subjectivity,
// no quality judgement — only "is this stalled run still making progress, stuck,
// or in need of a human?". An out-of-enum value is an ERROR, never coerced.
type Advice string

const (
	// AdviceProgressing means the advisor judged the stalled-looking run to still be
	// making progress (e.g. a long compile/test that legitimately produces no output
	// for a while). It maps to Continue.
	AdviceProgressing Advice = "progressing"
	// AdviceStuck means the advisor judged the run wedged with no path forward (e.g.
	// an infinite retry loop, a hang). It maps to Kill.
	AdviceStuck Advice = "stuck"
	// AdviceNeedsHuman means the advisor judged the run to need human intervention
	// (e.g. waiting on an unanswerable prompt, an auth wall it cannot resolve). It
	// maps to Escalate.
	AdviceNeedsHuman Advice = "needs_human"
)

// Valid reports whether a is one of the three allowed verdicts. The advisor
// parser uses it to reject out-of-enum advice as an error rather than acting on a
// value the contract does not define.
func (a Advice) Valid() bool {
	switch a {
	case AdviceProgressing, AdviceStuck, AdviceNeedsHuman:
		return true
	default:
		return false
	}
}

// Signals is the deterministic evidence the sentinel reasons over for one
// assessment. It carries no LLM output: the advisor is consulted separately
// (only in the gray zone) via the injected Advisor, given LastOutput.
//
// The fields encode the two independent liveness axes ADR-0016 distinguishes:
// staleness (Elapsed / SinceActivity, "how long since something happened") and
// hard liveness (Alive, "is the process even up"). The sentinel combines them
// with its thresholds; it does not probe anything itself.
type Signals struct {
	// Elapsed is the total wall time the run has been going. It is the input to the
	// Layer-3 backstop (Elapsed >= MaxTotal -> Kill).
	Elapsed time.Duration
	// SinceActivity is how long since output last flowed (the Layer-1 staleness
	// axis, mirroring engine.HealthState.LastActivityTS). A small value is fresh
	// activity (Continue without the advisor); a large value with Alive=true is the
	// gray zone (consult the advisor).
	SinceActivity time.Duration
	// Alive reports whether the performer process is still up. A run that is NOT
	// alive and has no fresh activity is clearly dead (Layer-1 Kill); a run that IS
	// alive but stalled is the gray zone (Layer-2).
	Alive bool
	// LastOutput is the tail of the performer's recent output, handed to the advisor
	// in the gray zone so it can judge progressing/stuck/needs_human. It is only
	// read when the advisor is actually consulted.
	LastOutput string
}

// Config holds the sentinel's deterministic thresholds. They are the only tuning
// knobs; the layer PRECEDENCE itself is fixed in code (backstop first) so it can
// never be configured away.
type Config struct {
	// MaxTotal is the absolute ceiling (Layer-3 backstop). When Elapsed reaches it
	// the run is Killed regardless of any advisor verdict — this is the structural
	// guarantee that the LLM can never wait forever. A non-positive MaxTotal
	// disables the backstop (the caller's own develop timeout then bounds the run);
	// it is intended to be SET in production.
	MaxTotal time.Duration
	// FreshActivity is the Layer-1 freshness window: a run whose output flowed more
	// recently than this is Continue WITHOUT consulting the advisor. It keeps the
	// advisor rare (no LLM load on healthy runs). Defaults to defaultFreshActivity.
	FreshActivity time.Duration
	// GraceUnsure is how long a run must be stalled (no fresh activity) while still
	// Alive before the gray-zone advisor is consulted. Below it, a stalled-but-not-
	// yet-grace run is treated as still fresh (Continue). Defaults to
	// defaultGraceUnsure. It must be >= FreshActivity to be meaningful; if it is
	// smaller it is treated as FreshActivity.
	GraceUnsure time.Duration
}

// Default thresholds. They are deliberately conservative: the advisor is only
// consulted after a real stall, and the backstop is disabled unless the caller
// sets a ceiling (the develop timeout otherwise bounds the run).
const (
	defaultFreshActivity = 90 * time.Second
	defaultGraceUnsure   = 5 * time.Minute
)

// withDefaults returns a copy of cfg with zero/negative tuning fields filled in.
// MaxTotal is left as-is (non-positive intentionally disables the backstop).
func (cfg Config) withDefaults() Config {
	if cfg.FreshActivity <= 0 {
		cfg.FreshActivity = defaultFreshActivity
	}
	if cfg.GraceUnsure <= 0 {
		cfg.GraceUnsure = defaultGraceUnsure
	}
	if cfg.GraceUnsure < cfg.FreshActivity {
		cfg.GraceUnsure = cfg.FreshActivity
	}
	return cfg
}

// Sentinel is the 3-layer liveness decider. Construct it with New; it is
// immutable and safe for concurrent use (Assess only reads cfg + calls the
// advisor, which must itself be safe for the caller's concurrency).
type Sentinel struct {
	cfg     Config
	advisor Advisor
}

// New returns a Sentinel with the given thresholds and an OPTIONAL gray-zone
// advisor. A nil advisor is valid and supported: the sentinel then runs Layers
// 1+3 only (the pre-2C-1 behavior) and the gray zone falls back to the
// conservative Continue-until-backstop. Tuning thresholds are defaulted.
func New(cfg Config, advisor Advisor) *Sentinel {
	return &Sentinel{cfg: cfg.withDefaults(), advisor: advisor}
}

// Result is the structured outcome of an assessment: the Decision plus which
// layer produced it and a human-readable reason. The caller logs/emits Layer +
// Reason for observability; the Decision drives the action.
type Result struct {
	// Decision is the action to take.
	Decision Decision
	// Layer is which layer decided: 1, 2, or 3. It makes the precedence auditable
	// (a Kill at Layer 3 is the backstop; a Kill at Layer 2 is advisor-stuck).
	Layer int
	// Advice is the gray-zone advisor's verdict when Layer 2 was consulted; empty
	// otherwise. It is carried for observability, not re-interpreted by the caller.
	Advice Advice
	// Reason is a short factual explanation of the decision.
	Reason string
}

// Assess runs the 3-layer decision over the deterministic signals, consulting the
// gray-zone advisor ONLY when the deterministic base is unsure (alive but
// stalled). The precedence is FIXED and the backstop is checked FIRST:
//
//  1. Layer-3 backstop: Elapsed >= MaxTotal -> Kill. Evaluated before the advisor
//     is ever consulted, so a "progressing" advisor verdict can NEVER override the
//     absolute ceiling. THIS is the DF-difference, made structural by ordering.
//  2. Layer-1 base: fresh activity (SinceActivity < FreshActivity) -> Continue
//     WITHOUT the advisor; clearly dead (not Alive and not in the gray window) ->
//     Kill. Only an alive-but-stalled run falls through.
//  3. Layer-2 gray zone: Alive && SinceActivity >= GraceUnsure -> consult the
//     advisor. progressing -> Continue, stuck -> Kill, needs_human -> Escalate.
//     No advisor or an advisor error -> conservative Continue (the backstop in
//     (1) is what eventually bounds such a run; the sentinel never spuriously
//     escalates or kills on advisor trouble).
//
// ctx bounds the advisor call (the gray zone is the only place ctx is used).
func (s *Sentinel) Assess(ctx context.Context, sig Signals) Result {
	// --- Layer 3: deterministic backstop, checked FIRST so it ALWAYS wins. ---
	// A positive MaxTotal that has been reached kills the run before any other
	// layer runs and, crucially, before the advisor is consulted: the advisor can
	// therefore never make the run wait past this ceiling.
	if s.cfg.MaxTotal > 0 && sig.Elapsed >= s.cfg.MaxTotal {
		return Result{
			Decision: Kill,
			Layer:    3,
			Reason:   "backstop: elapsed reached absolute ceiling (MaxTotal); killing regardless of advisor",
		}
	}

	// --- Layer 1: deterministic base. ---
	// Fresh activity is the cheap common case: keep going WITHOUT spending an
	// advisor call.
	if sig.SinceActivity < s.cfg.FreshActivity {
		return Result{
			Decision: Continue,
			Layer:    1,
			Reason:   "base: fresh activity within freshness window",
		}
	}
	// Stalled AND the process is not even alive, and not yet in the gray window:
	// clearly dead -> Kill. (A still-alive process always defers to the gray zone.)
	if !sig.Alive {
		return Result{
			Decision: Kill,
			Layer:    1,
			Reason:   "base: process not alive and output stalled (clearly dead)",
		}
	}

	// --- Layer 2: gray zone (alive but stalled). ---
	// Between FreshActivity and GraceUnsure the run is stalled but not yet long
	// enough to spend an advisor call on: treat as Continue (Layer-1 grace),
	// bounded by the backstop.
	if sig.SinceActivity < s.cfg.GraceUnsure {
		return Result{
			Decision: Continue,
			Layer:    1,
			Reason:   "base: stalled but within grace window (advisor not yet consulted)",
		}
	}

	// Alive + stalled past the grace window: this is the RARE gray zone where the
	// advisor adds value. No advisor -> conservative Continue (backstop bounds it).
	if s.advisor == nil {
		return Result{
			Decision: Continue,
			Layer:    2,
			Reason:   "gray zone: no advisor; conservative continue until backstop",
		}
	}

	advice, reason, err := s.advisor.Advise(ctx, sig.LastOutput)
	if err != nil {
		// Advisor trouble must NEVER spuriously escalate or kill: fall back to the
		// conservative Continue. The Layer-3 backstop still bounds the run, so a
		// permanently-broken advisor cannot let a hung run wait forever.
		return Result{
			Decision: Continue,
			Layer:    2,
			Reason:   "gray zone: advisor error; conservative continue until backstop: " + err.Error(),
		}
	}

	switch advice {
	case AdviceProgressing:
		return Result{Decision: Continue, Layer: 2, Advice: advice, Reason: graymsg("progressing -> continue", reason)}
	case AdviceStuck:
		return Result{Decision: Kill, Layer: 2, Advice: advice, Reason: graymsg("stuck -> kill", reason)}
	case AdviceNeedsHuman:
		return Result{Decision: Escalate, Layer: 2, Advice: advice, Reason: graymsg("needs_human -> escalate", reason)}
	default:
		// Out-of-enum advice (the parser should have rejected it, but defend in
		// depth): treat as advisor trouble -> conservative Continue.
		return Result{
			Decision: Continue,
			Layer:    2,
			Reason:   "gray zone: advisor returned out-of-enum advice; conservative continue until backstop",
		}
	}
}

// graymsg builds a gray-zone reason string, appending the advisor's one-line
// rationale when present.
func graymsg(head, reason string) string {
	if reason == "" {
		return "gray zone: advisor " + head
	}
	return "gray zone: advisor " + head + " (" + reason + ")"
}
