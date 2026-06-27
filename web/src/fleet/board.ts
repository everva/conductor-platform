// board.ts — the PURE model behind the Command Center Kanban (agent-native
// redesign, E1). It buckets every task across every project into the four
// lifecycle columns a director watches — Ready, Running, Needs Review, Done — and
// enriches each card with the LIVE truth the gateway already streams:
//   * which host is running it (from the repo lease, the live work signal);
//   * its current activity phase (from the most recent lifecycle event);
//   * its branch-vs-base diff size (from the most recent KindDiff event, 4C-1).
//
// It is deliberately pure (no React, no I/O): the same data useFleet already
// aggregates (tasksByProject + leasesByProject + the WS event buffer) goes in, a
// renderable board model comes out. That keeps the bucketing unit-testable and the
// component a thin view. No new gateway surface is needed (B3 of the redesign plan):
// the board is composed entirely from /projects + /tasks + /status leases + /ws.
import type { Event, Lease, Task } from "../api/types.ts";

// BoardColumnKey is the lifecycle column a task sits in. "needs-review" is the
// director's action lane (awaiting-approval OR blocked) and is surfaced first.
export type BoardColumnKey = "ready" | "running" | "needs-review" | "done";

// BOARD_COLUMNS is the ordered, labelled column set the board renders left→right.
export const BOARD_COLUMNS: readonly { key: BoardColumnKey; label: string }[] = [
  { key: "ready", label: "Ready" },
  { key: "running", label: "Running" },
  { key: "needs-review", label: "Needs Review" },
  { key: "done", label: "Done" },
];

// BoardDiff is the per-card diff size derived from the latest KindDiff event.
export interface BoardDiff {
  files: number;
  additions: number;
  deletions: number;
}

// BoardCard is one task enriched for the board. `host` is the lease holder (the
// live "where it runs" signal); `livePhase` is a present-tense activity label
// ("developing"/"verifying"/…) or null; `liveActivity` is the RICH line the performer
// surfaced (📖/✍️/🔎/🤔 — what claude is doing right now, from the latest progress pulse's
// payload.detail) or null; `diff` is the latest diff size or null.
export interface BoardCard {
  task: Task;
  host: string | null;
  livePhase: string | null;
  liveActivity: string | null;
  diff: BoardDiff | null;
}

// BoardModel is the renderable board: cards grouped by column + the top-strip
// counts a director scans first (running / needs-your-review / blocked).
export interface BoardModel {
  columns: Record<BoardColumnKey, BoardCard[]>;
  counts: { running: number; needsReview: number; blocked: number };
}

// phaseLabel maps an event phase to a present-tense activity label for a card, or
// null for phases that are not a meaningful "currently doing X" signal.
function phaseLabel(phase: string): string | null {
  switch (phase) {
    case "develop":
      return "developing";
    case "verify":
      return "verifying";
    case "review":
      return "reviewing";
    case "merge":
      return "merging";
    case "plan":
      return "planning";
    case "test":
      return "testing";
    default:
      return null;
  }
}

// num coerces an unknown payload value to a finite number, defaulting to 0 — the
// diff payload arrives as Record<string, unknown> (JSON), so each field is widened.
function num(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

// parseDiff sums a KindDiff event's per-file additions/deletions into a BoardDiff.
// The payload shape mirrors events.DiffSummary (4C-1): files[]{additions,deletions}.
// A malformed/absent files array yields null so a card simply shows no diff.
function parseDiff(ev: Event): BoardDiff | null {
  const files = ev.payload["files"];
  if (!Array.isArray(files)) {
    return null;
  }
  let additions = 0;
  let deletions = 0;
  for (const f of files) {
    if (f && typeof f === "object") {
      const rec = f as Record<string, unknown>;
      additions += num(rec["additions"]);
      deletions += num(rec["deletions"]);
    }
  }
  return { files: files.length, additions, deletions };
}

// latestByTask returns, per task id, the most recent event (by ISO ts) matching
// `accept`. The event buffer is small (the WS cap), so a linear scan is fine.
function latestByTask(
  events: readonly Event[],
  accept: (ev: Event) => boolean,
): Map<string, Event> {
  const out = new Map<string, Event>();
  for (const ev of events) {
    if (!ev.task || !accept(ev)) {
      continue;
    }
    const prev = out.get(ev.task);
    // ISO-8601 timestamps compare lexicographically; keep the newest.
    if (!prev || ev.ts >= prev.ts) {
      out.set(ev.task, ev);
    }
  }
  return out;
}

// leaseHostByTask indexes every active lease by its task id → holding host, so a
// card can show the host actually running it (the live work signal, not a stored
// status that may lag the brief running window).
function leaseHostByTask(leasesByProject: Record<string, Lease[]>): Map<string, string> {
  const out = new Map<string, string>();
  for (const leases of Object.values(leasesByProject)) {
    for (const l of leases) {
      if (l.task_id) {
        out.set(l.task_id, l.host_id);
      }
    }
  }
  return out;
}

// columnFor decides a task's lifecycle column. A task holding an active lease is
// RUNNING regardless of its stored status (the lease is the live truth, and the
// in-store "running" window is brief). Otherwise it maps by status: awaiting-
// approval/blocked → the director's Needs-Review lane; done/rejected → Done (both
// terminal — a rejected held task left the queue WITHOUT merging); everything else
// (todo/ready/unknown) → Ready.
function columnFor(task: Task, leased: boolean): BoardColumnKey {
  if (leased) {
    return "running";
  }
  switch (task.status) {
    case "running":
      return "running";
    case "awaiting-approval":
    case "blocked":
      return "needs-review";
    case "done":
    case "rejected":
      return "done";
    default:
      return "ready";
  }
}

// needsReviewCount returns how many tasks sit in the director's Needs-Review lane
// (awaiting-approval or blocked, and not currently running on a lease) — reusing
// the SAME bucketing buildBoard uses (leaseHostByTask + columnFor), so the shell's
// persistent review badge always matches the board's Needs-Review column exactly.
export function needsReviewCount(
  tasksByProject: Record<string, Task[]>,
  leasesByProject: Record<string, Lease[]>,
): number {
  const hostByTask = leaseHostByTask(leasesByProject);
  let n = 0;
  for (const tasks of Object.values(tasksByProject)) {
    for (const t of tasks) {
      if (columnFor(t, hostByTask.has(t.id)) === "needs-review") {
        n++;
      }
    }
  }
  return n;
}

// buildBoard composes the renderable board model from the useFleet snapshot slices.
// Cards within a column are ordered by project then task id for a stable layout.
export function buildBoard(
  tasksByProject: Record<string, Task[]>,
  leasesByProject: Record<string, Lease[]>,
  events: readonly Event[],
): BoardModel {
  const hostByTask = leaseHostByTask(leasesByProject);
  const lastEvent = latestByTask(events, () => true);
  const lastDiff = latestByTask(events, (ev) => ev.kind === "diff");

  const columns: Record<BoardColumnKey, BoardCard[]> = {
    ready: [],
    running: [],
    "needs-review": [],
    done: [],
  };

  const allTasks: Task[] = [];
  for (const tasks of Object.values(tasksByProject)) {
    for (const t of tasks) {
      allTasks.push(t);
    }
  }
  allTasks.sort((a, b) =>
    a.project_id === b.project_id
      ? a.id.localeCompare(b.id)
      : a.project_id.localeCompare(b.project_id),
  );

  let blocked = 0;
  for (const task of allTasks) {
    const host = hostByTask.get(task.id) ?? null;
    const leased = host !== null;
    const col = columnFor(task, leased);
    if (task.status === "blocked") {
      blocked++;
    }
    const ev = lastEvent.get(task.id);
    const diffEv = lastDiff.get(task.id);
    // The rich activity line rides on the latest progress pulse's payload.detail (the agent
    // streams claude's 📖/🔎/🤔 from its transcript every ~20s). Surface it verbatim when present.
    const detail =
      ev && typeof ev.payload.detail === "string" && ev.payload.detail !== ""
        ? ev.payload.detail
        : null;
    columns[col].push({
      task,
      host,
      livePhase: ev ? phaseLabel(ev.phase) : null,
      liveActivity: detail,
      diff: diffEv ? parseDiff(diffEv) : null,
    });
  }

  return {
    columns,
    counts: {
      running: columns.running.length,
      needsReview: columns["needs-review"].length,
      blocked,
    },
  };
}
