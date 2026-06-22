// Unit tests for the pure session model (E2): the deterministic Verifier verdict
// (B1 — per-gate checks, the trust differentiator), the bounded diff, and the
// activity timeline — all derived from a task's event buffer.
import { describe, expect, it } from "vitest";
import { buildTimeline, parseDiff, parseVerdict, stepReplay } from "./session.ts";
import type { Event } from "../api/types.ts";

function ev(p: Partial<Event> & Pick<Event, "kind" | "phase">): Event {
  return {
    id: `ev-${p.kind}-${p.phase}-${p.ts ?? "0"}`,
    ts: "2026-06-20T10:00:00Z",
    project: "proj",
    task: "T-1",
    payload: {},
    ...p,
  };
}

describe("parseVerdict", () => {
  it("extracts the per-gate checks from the latest decision carrying checks", () => {
    const events: Event[] = [
      ev({
        kind: "decision",
        phase: "review",
        ts: "2026-06-20T10:05:00Z",
        payload: {
          result: "pass",
          checks: [
            { name: "go build", result: "pass", evidence: "exit 0" },
            { name: "go test", result: "pass", evidence: "exit 0" },
            { name: "hidden holdout", result: "pass", evidence: "exit 0" },
          ],
        },
      }),
    ];
    const v = parseVerdict(events);
    expect(v?.result).toBe("pass");
    expect(v?.checks).toHaveLength(3);
    expect(v?.checks[1]).toEqual({ name: "go test", result: "pass", evidence: "exit 0" });
  });

  it("surfaces a failing check + evidence (the WHY) on changes-requested", () => {
    const v = parseVerdict([
      ev({
        kind: "decision",
        phase: "review",
        payload: {
          result: "changes-requested",
          checks: [{ name: "go test", result: "fail", evidence: "FAIL: expected 2 got 1" }],
        },
      }),
    ]);
    expect(v?.result).toBe("changes-requested");
    expect(v?.checks[0]).toMatchObject({ result: "fail", evidence: "FAIL: expected 2 got 1" });
  });

  it("ignores retry/approved decisions that carry no checks", () => {
    expect(
      parseVerdict([ev({ kind: "decision", phase: "review", payload: { result: "approved" } })]),
    ).toBeNull();
  });

  it("returns null when there is no decision event", () => {
    expect(parseVerdict([ev({ kind: "started", phase: "develop" })])).toBeNull();
  });

  it("asOf replays the verdict in effect at that moment, not the latest (E4)", () => {
    const events: Event[] = [
      ev({
        kind: "decision", phase: "review", ts: "2026-06-20T10:05:00Z",
        payload: { result: "changes-requested", checks: [{ name: "go test", result: "fail", evidence: "x" }] },
      }),
      ev({
        kind: "decision", phase: "review", ts: "2026-06-20T10:09:00Z",
        payload: { result: "pass", checks: [{ name: "go test", result: "pass", evidence: "ok" }] },
      }),
    ];
    expect(parseVerdict(events)?.result).toBe("pass"); // live → latest
    expect(parseVerdict(events, "2026-06-20T10:06:00Z")?.result).toBe("changes-requested");
    expect(parseVerdict(events, "2026-06-20T10:00:00Z")).toBeNull(); // before any decision
  });
});

describe("parseDiff", () => {
  it("sums per-file additions/deletions from the latest KindDiff event", () => {
    const d = parseDiff([
      ev({
        kind: "diff",
        phase: "review",
        payload: {
          branch: "conductor/T-1",
          base: "develop",
          truncated: false,
          patch: "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-x\n+y\n",
          files: [
            { path: "a.go", status: "M", additions: 10, deletions: 2 },
            { path: "b.go", status: "A", additions: 5, deletions: 0 },
          ],
        },
      }),
    ]);
    expect(d).toMatchObject({ branch: "conductor/T-1", base: "develop", additions: 15, deletions: 2 });
    expect(d?.files).toHaveLength(2);
    expect(d?.patch).toContain("+y");
  });

  it("returns null with no diff event", () => {
    expect(parseDiff([ev({ kind: "started", phase: "verify" })])).toBeNull();
  });

  it("asOf replays the diff in effect at that moment (E4)", () => {
    const mk = (ts: string, additions: number) =>
      ev({ kind: "diff", phase: "review", ts, payload: { files: [{ path: "a.go", status: "M", additions, deletions: 0 }] } });
    const events = [mk("2026-06-20T10:05:00Z", 3), mk("2026-06-20T10:08:00Z", 10)];
    expect(parseDiff(events)?.additions).toBe(10); // live → latest
    expect(parseDiff(events, "2026-06-20T10:06:00Z")?.additions).toBe(3); // as of earlier
    expect(parseDiff(events, "2026-06-20T10:00:00Z")).toBeNull(); // before any diff
  });
});

describe("buildTimeline", () => {
  it("maps events to readable phase·kind labels in order", () => {
    const t = buildTimeline([
      ev({ kind: "started", phase: "develop", ts: "2026-06-20T10:00:00Z" }),
      ev({ kind: "decision", phase: "review", ts: "2026-06-20T10:05:00Z" }),
      ev({ kind: "merge", phase: "merge", ts: "2026-06-20T10:06:00Z" }),
    ]);
    expect(t.map((e) => e.label)).toEqual([
      "Develop · started",
      "Review · verdict",
      "Merge · merged",
    ]);
  });

  it("derives a payload summary per entry for the replay log (E4)", () => {
    const t = buildTimeline([
      ev({ kind: "progress", phase: "develop", payload: { pct: 42 } }),
      ev({
        kind: "diff", phase: "review",
        payload: { files: [{ path: "a.go", additions: 10, deletions: 2 }, { path: "b.go", additions: 5, deletions: 0 }] },
      }),
      ev({ kind: "decision", phase: "review", payload: { result: "pass", checks: [] } }),
      ev({ kind: "started", phase: "develop" }),
    ]);
    expect(t[0]!.summary).toBe("42%");
    // Glyph-tolerant on the minus (source uses U+2212): files +adds/−dels.
    expect(t[1]!.summary).toMatch(/^2 files \+15\/.2$/);
    expect(t[2]!.summary).toBe("pass");
    expect(t[3]!.summary).toBe("");
  });
});

describe("stepReplay (N4 arrow-key time-travel)", () => {
  const tl = [{ ts: "t1" }, { ts: "t2" }, { ts: "t3" }];

  it("back from live selects the newest entry; forward from live stays live", () => {
    expect(stepReplay(tl, null, "back")).toBe("t3");
    expect(stepReplay(tl, null, "forward")).toBe(null);
  });

  it("steps back to older entries and forward to newer ones", () => {
    expect(stepReplay(tl, "t3", "back")).toBe("t2");
    expect(stepReplay(tl, "t2", "back")).toBe("t1");
    expect(stepReplay(tl, "t1", "forward")).toBe("t2");
    expect(stepReplay(tl, "t2", "forward")).toBe("t3");
  });

  it("clamps at the oldest; forward past the newest returns to live", () => {
    expect(stepReplay(tl, "t1", "back")).toBe("t1");
    expect(stepReplay(tl, "t3", "forward")).toBe(null);
  });

  it("snaps a stale selection to the newest; an empty timeline is a no-op", () => {
    expect(stepReplay(tl, "gone", "back")).toBe("t3");
    expect(stepReplay(tl, "gone", "forward")).toBe("t3");
    expect(stepReplay([], null, "back")).toBe(null);
    expect(stepReplay([], "x", "forward")).toBe("x");
  });
});
