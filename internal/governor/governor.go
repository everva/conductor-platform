// Package governor is the platform's admission control: it bounds concurrency
// BEFORE the conductor picks up new work (ADR-0008). The limits below are
// enforced, each derived from an authoritative source rather than a cached
// counter (ADR-0010 "derive from source"):
//
//   - Global cap: at most N tasks running simultaneously across ALL projects.
//     The current count is the number of active leases in the StateStore
//     (ListLeases) — a lease is taken per running task (ADR-0008 lease = global
//     cap + repo-per-1), so the lease table IS the live concurrency, with no
//     parallel counter that can drift.
//   - Repo-per-1: a project that already holds a lease is busy and admits no new
//     task. This mirrors the AcquireLease rule the registry enforces later, but
//     the governor surfaces it up front as a clear, structured reason rather than
//     letting the lease acquire fail mid-tick.
//   - Per-host cap (multi-host, agent-per-host, ADR-0024): a single host admits no
//     more than HostCap concurrent tasks of its OWN, so a host never exceeds its
//     own capacity even when the global cap (across all hosts) still has room. The
//     count is the number of active leases whose HostID is THIS host, taken from
//     the SAME lease read as the global count. HostCap <= 0 DISABLES it (only the
//     global + repo + load limits apply — the pre-2B-4 behavior).
//   - Host-load ceiling: when the machine is already saturated, no new work is
//     admitted. Load is read through an injectable LoadProbe (deterministic in
//     tests) and normalized by CPU count; admission is denied when normalized
//     1-minute load exceeds a configurable ceiling.
//
// The Governor is small, deterministic, and side-effect free: Admit reads the
// store + probe and returns a Decision; it takes no lease and mutates nothing.
package governor

import (
	"context"
	"errors"
	"fmt"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Reason is the structured cause of an admission decision. On admit it is
// ReasonAdmit; on deny it names which of the three limits tripped, so a caller
// can log the cause without parsing free text.
type Reason string

const (
	// ReasonAdmit means all three limits passed and work may start.
	ReasonAdmit Reason = "admit"
	// ReasonDenyGlobalCap means the global concurrent-task cap is already reached
	// (active leases >= cap).
	ReasonDenyGlobalCap Reason = "deny:global-cap"
	// ReasonDenyRepoBusy means the project already holds an active lease
	// (repo-per-1, ADR-0008).
	ReasonDenyRepoBusy Reason = "deny:repo-busy"
	// ReasonDenyHostCap means THIS host already runs as many concurrent tasks as
	// its per-host cap allows (active leases on this host >= HostCap). It bounds a
	// single host's own capacity independently of the global cap (multi-host,
	// agent-per-host, ADR-0024): a Mac and a Linux box can carry different caps.
	ReasonDenyHostCap Reason = "deny:host-cap"
	// ReasonDenyHostLoad means normalized host load is above the ceiling.
	ReasonDenyHostLoad Reason = "deny:host-load"
)

// Decision is the result of Admit: whether to admit new work and the structured
// reason. When Admit is false, Reason names the limit that tripped; the
// numeric fields carry the observed-vs-limit values for logging.
type Decision struct {
	// Admit reports whether new work may start.
	Admit bool
	// Reason is the structured cause (admit, or which limit denied).
	Reason Reason
	// ActiveLeases is the live count of held leases at decision time (the global
	// concurrency, derived from the StateStore).
	ActiveLeases int
	// GlobalCap is the configured maximum concurrent tasks.
	GlobalCap int
	// NormalizedLoad is the 1-minute load average divided by CPU count.
	NormalizedLoad float64
	// LoadCeiling is the configured normalized-load ceiling.
	LoadCeiling float64
	// HostActiveLeases is the live count of held leases on THIS host (g.hostID) at
	// decision time, derived from the same lease read as ActiveLeases. It is 0 when
	// the per-host cap is disabled (HostCap <= 0).
	HostActiveLeases int
	// HostCap is the configured per-host concurrent-task cap (0 = disabled).
	HostCap int
}

// LoadProbe reports the host's current 1-minute load average. It is an interface
// so tests can inject a deterministic value and so platforms that cannot read
// load can default to permissive (ok=false). Implementations must not block.
type LoadProbe interface {
	// Load1 returns the 1-minute load average. ok is false when the platform
	// cannot report load, in which case the governor treats the host-load limit
	// as permissive (it never false-denies on an unreadable platform).
	Load1() (load float64, ok bool)
}

// Config holds the governor's tunables. The zero value is intentionally NOT
// usable; construct a Config via DefaultConfig and override fields, or pass one
// to New which fills in non-positive fields with the defaults.
type Config struct {
	// GlobalCap is the maximum number of tasks that may run concurrently across
	// all projects. Must be >= 1; New substitutes DefaultGlobalCap if not.
	GlobalCap int
	// LoadCeiling is the maximum normalized 1-minute load (loadavg / NumCPU)
	// at which new work is still admitted. Must be > 0; New substitutes
	// DefaultLoadCeiling if not.
	LoadCeiling float64
	// HostID identifies THIS host (which host am I) so the per-host cap can count
	// only the leases held on this host. Empty when there is no single owning host
	// (single-process dev); the per-host cap is then effectively inert because no
	// lease matches an empty HostID under normal operation.
	HostID string
	// HostCap is the per-host concurrent-task cap: the maximum number of tasks that
	// may run on THIS host (HostID) simultaneously, independent of the global cap
	// across all hosts (multi-host, agent-per-host, ADR-0024). HostCap <= 0 DISABLES
	// the per-host cap (only global + repo-per-1 + host-load apply — today's
	// behavior). New does NOT substitute a default for a non-positive HostCap: a
	// non-positive value is the explicit "unlimited per host" setting.
	HostCap int
}

const (
	// DefaultGlobalCap is the default global concurrency cap (ADR-0008 "global
	// cap ~3-4 worker"). It bounds simultaneous tasks across all projects.
	DefaultGlobalCap = 4
	// DefaultLoadCeiling is the default normalized-load ceiling. It is set
	// permissively above 1.0 so a normal dev machine (load ~= core count) is not
	// false-denied; the ADR's "load-41 dersi" only trips well past saturation.
	DefaultLoadCeiling = 2.0
)

// DefaultConfig returns a Config populated with the package defaults.
func DefaultConfig() Config {
	return Config{GlobalCap: DefaultGlobalCap, LoadCeiling: DefaultLoadCeiling}
}

// Governor is the admission controller. Construct it with New; it holds the
// store seam (for the lease-derived global/repo checks), the load probe, and the
// resolved config. Admit is the only behavior and it is read-only.
type Governor struct {
	store statestore.StateStore
	probe LoadProbe
	cfg   Config
}

// New returns a Governor over the given store and load probe with the supplied
// config; non-positive config fields fall back to the package defaults so a
// half-filled Config is still sane. A nil probe falls back to the default
// system probe. It errors only on a nil store, which is unrecoverable.
func New(store statestore.StateStore, probe LoadProbe, cfg Config) (*Governor, error) {
	if store == nil {
		return nil, errors.New("governor: nil store")
	}
	if cfg.GlobalCap < 1 {
		cfg.GlobalCap = DefaultGlobalCap
	}
	if cfg.LoadCeiling <= 0 {
		cfg.LoadCeiling = DefaultLoadCeiling
	}
	// HostCap is intentionally NOT defaulted: a non-positive HostCap is the explicit
	// "per-host cap disabled" setting, not a half-filled field. HostID is likewise
	// left as supplied (empty = no owning host).
	if probe == nil {
		probe = SystemLoadProbe{}
	}
	return &Governor{store: store, probe: probe, cfg: cfg}, nil
}

// Admit decides whether the conductor may start new work for projectID. It is
// deterministic and side-effect free: it reads the lease table and the load
// probe and returns a Decision; it acquires nothing.
//
// The checks run in a fixed order so denials are stable:
//
//  1. repo-per-1 (the most specific, project-scoped rule),
//  2. global cap (lease-derived concurrency across ALL hosts),
//  3. per-host cap (lease-derived concurrency on THIS host; skipped when
//     HostCap <= 0, the disabled/backward-compatible setting),
//  4. host-load ceiling.
//
// The order is deterministic: a more specific/structural limit wins over a
// noisier environmental one (repo-busy beats the caps; both caps beat host-load).
// The first failing check sets the Decision's Reason; all clear yields ReasonAdmit.
func (g *Governor) Admit(ctx context.Context, projectID string) (Decision, error) {
	leases, err := g.store.ListLeases(ctx)
	if err != nil {
		return Decision{}, fmt.Errorf("governor: admit %q: list leases: %w", projectID, err)
	}

	// Derive every lease-based fact from the single ListLeases read: the live
	// global count, whether THIS project is already leased, and how many leases are
	// held on THIS host. No parallel counter, no second store round-trip.
	active := len(leases)
	repoBusy := false
	hostActive := 0
	hostCapOn := g.cfg.HostCap > 0
	for _, l := range leases {
		if l.ProjectID == projectID {
			repoBusy = true
		}
		if hostCapOn && l.HostID == g.cfg.HostID {
			hostActive++
		}
	}

	load, loadOK := g.probe.Load1()
	normalized := normalizedLoad(load, loadOK)

	d := Decision{
		ActiveLeases:     active,
		GlobalCap:        g.cfg.GlobalCap,
		NormalizedLoad:   normalized,
		LoadCeiling:      g.cfg.LoadCeiling,
		HostActiveLeases: hostActive,
		HostCap:          g.cfg.HostCap,
	}

	switch {
	case repoBusy:
		d.Reason = ReasonDenyRepoBusy
	case active >= g.cfg.GlobalCap:
		d.Reason = ReasonDenyGlobalCap
	case hostCapOn && hostActive >= g.cfg.HostCap:
		d.Reason = ReasonDenyHostCap
	case loadOK && normalized > g.cfg.LoadCeiling:
		d.Reason = ReasonDenyHostLoad
	default:
		d.Admit = true
		d.Reason = ReasonAdmit
	}
	return d, nil
}

// normalizedLoad returns loadavg divided by CPU count, or 0 when the probe could
// not report load (so an unreadable platform is permissive: 0 never exceeds a
// positive ceiling).
func normalizedLoad(load float64, ok bool) float64 {
	if !ok {
		return 0
	}
	n := numCPU()
	if n < 1 {
		n = 1
	}
	return load / float64(n)
}
