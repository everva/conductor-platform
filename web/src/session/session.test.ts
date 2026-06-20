// Unit tests for the pure session model (E2): the deterministic Verifier verdict
// (B1 — per-gate checks, the trust differentiator), the bounded diff, and the
// activity timeline — all derived from a task's event buffer.
import { describe, expect, it } from "vitest";
import { buildTimeline, parseDiff, parseVerdict } from "./session.ts";
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
});
