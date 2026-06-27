// Unit tests for the pure Command Center board model (E1). They prove the
// bucketing rules the director relies on: status→column, the lease→running
// override (live truth over a lagging stored status), the Needs-Review action
// lane (awaiting-approval + blocked), the top-strip counts, and the per-card
// enrichment (live phase + summed diff) derived from the WS event buffer.
import { describe, expect, it } from "vitest";
import { buildBoard, needsReviewCount } from "./board.ts";
import type { Event, Lease, Task } from "../api/types.ts";

function task(partial: Partial<Task> & Pick<Task, "id" | "project_id" | "status">): Task {
  return {
    lane: "web",
    tier: "T2",
    requires: [],
    deps: [],
    branch: "",
    scenario_id: partial.id,
    retry_count: 0,
    abort_requested: false,
    approved: false,
    ...partial,
  };
}

function ev(partial: Partial<Event> & Pick<Event, "task" | "phase" | "kind">): Event {
  return {
    id: `ev-${partial.task}-${partial.phase}-${partial.kind}`,
    ts: "2026-06-20T10:00:00Z",
    project: "p",
    payload: {},
    ...partial,
  };
}

describe("buildBoard bucketing", () => {
  it("maps each status to its lifecycle column", () => {
    const tasks = {
      p: [
        task({ id: "T-todo", project_id: "p", status: "todo" }),
        task({ id: "T-ready", project_id: "p", status: "ready" }),
        task({ id: "T-run", project_id: "p", status: "running" }),
        task({ id: "T-await", project_id: "p", status: "awaiting-approval" }),
        task({ id: "T-block", project_id: "p", status: "blocked" }),
        task({ id: "T-done", project_id: "p", status: "done" }),
        task({ id: "T-reject", project_id: "p", status: "rejected" }),
      ],
    };
    const board = buildBoard(tasks, {}, []);
    expect(board.columns.ready.map((c) => c.task.id)).toEqual(["T-ready", "T-todo"]);
    expect(board.columns.running.map((c) => c.task.id)).toEqual(["T-run"]);
    expect(board.columns["needs-review"].map((c) => c.task.id)).toEqual([
      "T-await",
      "T-block",
    ]);
    // done + rejected are both terminal → the Done column (a rejected held task left the queue
    // WITHOUT merging, so it is NOT in Needs-Review and must not read as a fresh Ready task).
    expect(board.columns.done.map((c) => c.task.id)).toEqual(["T-done", "T-reject"]);
  });

  it("treats a leased task as running regardless of stored status, with its host", () => {
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "ready" })] };
    const leases: Record<string, Lease[]> = {
      p: [{ project_id: "p", host_id: "host-mac", task_id: "T-1", acquired_at: "x" }],
    };
    const board = buildBoard(tasks, leases, []);
    expect(board.columns.ready).toHaveLength(0);
    expect(board.columns.running.map((c) => c.task.id)).toEqual(["T-1"]);
    expect(board.columns.running[0]?.host).toBe("host-mac");
  });

  it("counts running / needs-review / blocked for the top strip", () => {
    const tasks = {
      p: [
        task({ id: "T-run", project_id: "p", status: "running" }),
        task({ id: "T-await", project_id: "p", status: "awaiting-approval" }),
        task({ id: "T-block", project_id: "p", status: "blocked" }),
      ],
    };
    const board = buildBoard(tasks, {}, []);
    expect(board.counts).toEqual({ running: 1, needsReview: 2, blocked: 1 });
  });
});

describe("needsReviewCount", () => {
  it("counts awaiting-approval + blocked tasks across projects (the badge truth)", () => {
    const tasks = {
      web: [
        task({ id: "W-await", project_id: "web", status: "awaiting-approval" }),
        task({ id: "W-run", project_id: "web", status: "running" }),
      ],
      ios: [
        task({ id: "I-block", project_id: "ios", status: "blocked" }),
        task({ id: "I-done", project_id: "ios", status: "done" }),
      ],
    };
    expect(needsReviewCount(tasks, {})).toBe(2);
  });

  it("excludes a task that is actually running on a lease (live truth wins)", () => {
    // A leased task is RUNNING regardless of a stale awaiting-approval status, so it
    // must NOT inflate the review badge — matching the board's column exactly.
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "awaiting-approval" })] };
    const leases: Record<string, Lease[]> = {
      p: [{ project_id: "p", host_id: "host-1", task_id: "T-1", acquired_at: "x" }],
    };
    expect(needsReviewCount(tasks, leases)).toBe(0);
  });

  it("is zero when nothing needs review", () => {
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "running" })] };
    expect(needsReviewCount(tasks, {})).toBe(0);
  });
});

describe("buildBoard enrichment", () => {
  it("labels the live phase from the most recent event", () => {
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "running" })] };
    const events: Event[] = [
      ev({ task: "T-1", phase: "develop", kind: "started", ts: "2026-06-20T10:00:00Z" }),
      ev({ task: "T-1", phase: "verify", kind: "started", ts: "2026-06-20T10:01:00Z" }),
    ];
    const board = buildBoard(tasks, {}, events);
    expect(board.columns.running[0]?.livePhase).toBe("verifying");
  });

  it("surfaces the rich live activity from the latest progress pulse's payload.detail", () => {
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "running" })] };
    const events: Event[] = [
      ev({ task: "T-1", phase: "develop", kind: "started", ts: "2026-06-20T10:00:00Z" }),
      ev({
        task: "T-1",
        phase: "develop",
        kind: "progress",
        ts: "2026-06-20T10:00:30Z",
        payload: { step: "developing", detail: "📖 Okunuyor: optiway/src/app.ts" },
      }),
    ];
    const board = buildBoard(tasks, {}, events);
    expect(board.columns.running[0]?.liveActivity).toBe("📖 Okunuyor: optiway/src/app.ts");
  });

  it("leaves liveActivity null when the latest event carries no detail", () => {
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "running" })] };
    const board = buildBoard(tasks, {}, [
      ev({ task: "T-1", phase: "develop", kind: "started" }),
    ]);
    expect(board.columns.running[0]?.liveActivity).toBeNull();
  });

  it("sums per-file additions/deletions from the latest KindDiff event", () => {
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "awaiting-approval" })] };
    const events: Event[] = [
      ev({
        task: "T-1",
        phase: "review",
        kind: "diff",
        payload: {
          files: [
            { path: "a.go", status: "M", additions: 10, deletions: 2 },
            { path: "b.go", status: "A", additions: 5, deletions: 0 },
          ],
        },
      }),
    ];
    const board = buildBoard(tasks, {}, events);
    expect(board.columns["needs-review"][0]?.diff).toEqual({
      files: 2,
      additions: 15,
      deletions: 2,
    });
  });

  it("leaves diff null when no diff event and tolerates a malformed payload", () => {
    const tasks = { p: [task({ id: "T-1", project_id: "p", status: "ready" })] };
    const board = buildBoard(tasks, {}, [
      ev({ task: "T-1", phase: "review", kind: "diff", payload: { files: "nope" } }),
    ]);
    // malformed files → null, not a throw
    expect(board.columns.ready[0]?.diff).toBeNull();
  });
});
