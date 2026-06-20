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
export function parseVerdict(events: readonly Event[]): SessionVerdict | null {
  const ev = latest(
    events,
    (e) => e.kind === "decision" && Array.isArray(e.payload["checks"]),
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
// summing the per-file additions/deletions. Null until a diff arrives.
export function parseDiff(events: readonly Event[]): SessionDiff | null {
  const ev = latest(events, (e) => e.kind === "diff");
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

// buildTimeline maps the task's events (ascending) to readable timeline entries.
export function buildTimeline(events: readonly Event[]): TimelineEntry[] {
  return events.map((e) => ({
    id: e.id,
    ts: e.ts,
    phase: e.phase,
    kind: e.kind,
    label: `${PHASE_LABEL[e.phase] ?? e.phase} · ${KIND_LABEL[e.kind] ?? e.kind}`,
  }));
}
