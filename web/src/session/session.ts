// session.ts — the PURE model behind the agent-native Session view (redesign E2).
// Given a task's event stream (the useEventFeed buffer for {project, task}), it
// derives the three things a director reads when drilling into one agent run:
//   * the deterministic Verifier verdict — per-gate checks (the trust differentiator,
//     B1/ADR-0033): WHY the gate passed/failed, machine-proven, not an LLM's opinion;
//   * the branch-vs-base diff (B/4C-1 KindDiff): what actually changed;
//   * the activity timeline: the develop→verify→verdict→diff→merge/hold lifecycle.
// No React, no I/O — same events in, renderable model out, so it is unit-testable.
import type { Event } from "../api/types.ts";

export interface VerdictCheck {
  name: string;
  result: string; // "pass" | "fail"
  evidence: string;
}

// SessionVerdict is the deterministic gate decision: the overall result + the
// per-gate checks. Null until a verdict (KindDecision carrying `checks`) arrives.
export interface SessionVerdict {
  result: string; // "pass" | "changes-requested"
  checks: VerdictCheck[];
}

export interface SessionDiffFile {
  path: string;
  status: string; // M / A / D ...
  additions: number;
  deletions: number;
}

// SessionDiff is the bounded branch-vs-base diff from the latest KindDiff event.
export interface SessionDiff {
  branch: string;
  base: string;
  files: SessionDiffFile[];
  patch: string;
  truncated: boolean;
  additions: number; // summed across files
  deletions: number;
}

export interface TimelineEntry {
  id: string;
  ts: string;
  phase: string;
  kind: string;
  label: string;
  // summary is a one-line, payload-derived gloss for the entry ("3 files +66/−0",
  // "pass", "📖 Okunuyor: x.ts", a log line) so the timeline reads as a replay log, not bare labels.
  summary: string;
  // count is how many consecutive same-phase progress pulses this entry collapses (≥2 → the row
  // shows "×N"); undefined for a single, ungrouped entry. Grouping kills the repeated-pulse spam.
  count?: number;
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}
function num(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

// latest returns the most recent event (by ISO ts) matching pred, or null.
function latest(events: readonly Event[], pred: (e: Event) => boolean): Event | null {
  let out: Event | null = null;
  for (const e of events) {
    if (pred(e) && (!out || e.ts >= out.ts)) {
      out = e;
    }
  }
  return out;
}

// parseVerdict extracts the deterministic Verifier verdict from the latest
// KindDecision event that carries a `checks` array (B1). Retry/approved decisions
// (no checks) are ignored, so a verdict is the gate's real per-gate decision.
//
// `asOf` (an ISO ts cutoff) drives the session replay (E4): with it set, the
// verdict is the one that was in effect AT OR BEFORE that moment — null if the gate
// had not decided yet — so scrubbing the timeline shows the real history, not just
// the latest. Omitted → the latest verdict (live, today's behavior).
export function parseVerdict(
  events: readonly Event[],
  asOf?: string,
): SessionVerdict | null {
  const ev = latest(
    events,
    (e) =>
      e.kind === "decision" &&
      Array.isArray(e.payload["checks"]) &&
      (asOf === undefined || e.ts <= asOf),
  );
  if (!ev) {
    return null;
  }
  const raw = ev.payload["checks"];
  const checks: VerdictCheck[] = Array.isArray(raw)
    ? raw.map((c): VerdictCheck => {
        const rec = (c ?? {}) as Record<string, unknown>;
        return { name: str(rec["name"]), result: str(rec["result"]), evidence: str(rec["evidence"]) };
      })
    : [];
  return { result: str(ev.payload["result"]), checks };
}

// parseDiff extracts the bounded branch-vs-base diff from the latest KindDiff event,
// summing the per-file additions/deletions. Null until a diff arrives. `asOf` drives
// replay the same way parseVerdict does: the diff in effect at or before that moment.
export function parseDiff(
  events: readonly Event[],
  asOf?: string,
): SessionDiff | null {
  const ev = latest(
    events,
    (e) => e.kind === "diff" && (asOf === undefined || e.ts <= asOf),
  );
  if (!ev) {
    return null;
  }
  const filesRaw = ev.payload["files"];
  const files: SessionDiffFile[] = Array.isArray(filesRaw)
    ? filesRaw.map((f): SessionDiffFile => {
        const rec = (f ?? {}) as Record<string, unknown>;
        return {
          path: str(rec["path"]),
          status: str(rec["status"]),
          additions: num(rec["additions"]),
          deletions: num(rec["deletions"]),
        };
      })
    : [];
  return {
    branch: str(ev.payload["branch"]),
    base: str(ev.payload["base"]),
    files,
    patch: str(ev.payload["patch"]),
    truncated: ev.payload["truncated"] === true,
    additions: files.reduce((s, f) => s + f.additions, 0),
    deletions: files.reduce((s, f) => s + f.deletions, 0),
  };
}

const PHASE_LABEL: Record<string, string> = {
  plan: "Plan",
  develop: "Develop",
  test: "Test",
  verify: "Verify",
  review: "Review",
  merge: "Merge",
};
const KIND_LABEL: Record<string, string> = {
  started: "started",
  progress: "progress",
  log: "log",
  diff: "diff ready",
  decision: "verdict",
  health: "health",
  pr: "PR opened",
  merge: "merged",
  "intervention-needed": "needs review",
};

// summarize derives a one-line gloss from an event's payload for the timeline —
// what actually happened at that step, so the replay log is scannable. Unknown or
// payload-less kinds yield "" (the label alone carries them).
function summarize(e: Event): string {
  switch (e.kind) {
    case "diff": {
      const files = Array.isArray(e.payload["files"]) ? e.payload["files"] : [];
      let a = 0;
      let d = 0;
      for (const f of files) {
        if (f && typeof f === "object") {
          const r = f as Record<string, unknown>;
          a += num(r["additions"]);
          d += num(r["deletions"]);
        }
      }
      return `${files.length} file${files.length === 1 ? "" : "s"} +${a}/−${d}`;
    }
    case "decision":
      return str(e.payload["result"]);
    case "progress": {
      // The agent's live activity line (📖 Okunuyor / ✍️ Yazılıyor / 🔎 Aranıyor / 🤔 Düşünülüyor) —
      // what the performer is doing RIGHT NOW (develop_progress.go reads it from the claude
      // transcript). Falls back to a percent or "" when no rich detail rode along.
      const detail = str(e.payload["detail"]);
      if (detail) {
        return detail;
      }
      const pct = e.payload["pct"];
      return typeof pct === "number" ? `${pct}%` : "";
    }
    case "log":
      return str(e.payload["msg"]) || str(e.payload["message"]);
    case "pr": {
      const n = e.payload["number"];
      return typeof n === "number" ? `#${n}` : str(e.payload["url"]);
    }
    default:
      return "";
  }
}

// buildTimeline maps the task's events (ascending) to readable timeline entries, each with a
// payload-derived summary for the replay log. A RUN of consecutive progress pulses in the same
// phase is COLLAPSED into one entry (latest moment + activity + an ×N count) so the timeline reads
// "Develop · progress  📖 Okunuyor: x.ts  ×12" instead of 12 identical rows — the director asked not
// to be drowned in repeated pulses, while still seeing it is actively working. Every non-progress
// event (started/diff/decision/log/merge/…) stays its own entry.
export function buildTimeline(events: readonly Event[]): TimelineEntry[] {
  const out: TimelineEntry[] = [];
  for (const e of events) {
    const entry: TimelineEntry = {
      id: e.id,
      ts: e.ts,
      phase: e.phase,
      kind: e.kind,
      label: `${PHASE_LABEL[e.phase] ?? e.phase} · ${KIND_LABEL[e.kind] ?? e.kind}`,
      summary: summarize(e),
    };
    // The stable id stays the run's FIRST pulse (steady React key as the group grows); ts + summary
    // advance to the newest so liveness + replay land on "now".
    const prev = out[out.length - 1];
    if (prev && prev.kind === "progress" && e.kind === "progress" && prev.phase === e.phase) {
      prev.ts = entry.ts;
      prev.summary = entry.summary;
      prev.count = (prev.count ?? 1) + 1;
      continue;
    }
    out.push(entry);
  }
  return out;
}

// SessionActivity is the newest live-activity pulse: WHAT the performer is doing + WHEN. It drives
// the session's liveness signal so the director is never blind to whether the claude instance is
// alive or has stalled.
export interface SessionActivity {
  ts: string;
  detail: string; // "📖 Okunuyor: x.ts" — "" when the pulse carried no rich detail
}

// latestActivity returns the most recent progress (or started) pulse, or null before any activity.
export function latestActivity(events: readonly Event[]): SessionActivity | null {
  const ev = latest(events, (e) => e.kind === "progress" || e.kind === "started");
  if (!ev) {
    return null;
  }
  return { ts: ev.ts, detail: str(ev.payload["detail"]) };
}

/**
 * stepReplay computes the next replay timestamp when stepping the Activity timeline with the
 * arrow keys (native-IDE N4 time-travel; reuses the E4 replay seam). `timeline` is chronological
 * (oldest first); `current` is the pinned ts or null (LIVE = following the newest). "back" moves
 * to an older entry, "forward" to a newer one — and stepping forward past the newest returns to
 * LIVE (null). Positions are [e0 … e(n-1), LIVE]: from LIVE, "back" selects the newest entry;
 * from the oldest, "back" clamps; a `current` ts no longer in the window (stale) snaps to the
 * newest. Pure (reads only `ts`) → unit-tested; the keydown handler just maps ←/→ onto it.
 */
export function stepReplay(
  timeline: readonly { ts: string }[],
  current: string | null,
  direction: "back" | "forward",
): string | null {
  if (timeline.length === 0) {
    return current;
  }
  const idx = current === null ? timeline.length : timeline.findIndex((e) => e.ts === current);
  if (idx === -1) {
    // Stale selection (the pinned entry aged out of the window) → snap to the newest.
    return timeline[timeline.length - 1]?.ts ?? null;
  }
  if (direction === "back") {
    return timeline[Math.max(0, idx - 1)]?.ts ?? null;
  }
  const next = idx + 1;
  return next >= timeline.length ? null : (timeline[next]?.ts ?? null);
}
