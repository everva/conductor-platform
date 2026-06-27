// Unit tests for mergeById — the pure dedupe/sort/cap helper at the heart of the
// event feed. Stable ids + ts so assertions are deterministic.
import { describe, expect, it } from "vitest";
import { collapseProgress, mergeById } from "./useEventFeed.ts";
import type { Event } from "../api/types.ts";

function ev(id: string, ts: string): Event {
  return {
    id,
    ts,
    project: "p",
    task: "t",
    phase: "develop",
    kind: "progress",
    payload: {},
  };
}

describe("mergeById", () => {
  it("dedupes by id, keeping the first occurrence", () => {
    const base = [ev("a", "2026-01-01T00:00:01Z")];
    const incoming = [ev("a", "2026-01-01T00:00:01Z"), ev("b", "2026-01-01T00:00:02Z")];
    const out = mergeById(base, incoming, 100);
    expect(out.map((e) => e.id)).toEqual(["a", "b"]);
  });

  it("sorts ascending by ts", () => {
    const out = mergeById(
      [],
      [ev("c", "2026-01-01T00:00:03Z"), ev("a", "2026-01-01T00:00:01Z"), ev("b", "2026-01-01T00:00:02Z")],
      100,
    );
    expect(out.map((e) => e.id)).toEqual(["a", "b", "c"]);
  });

  it("caps to the most-recent N, dropping the oldest", () => {
    const out = mergeById(
      [],
      [
        ev("a", "2026-01-01T00:00:01Z"),
        ev("b", "2026-01-01T00:00:02Z"),
        ev("c", "2026-01-01T00:00:03Z"),
      ],
      2,
    );
    expect(out.map((e) => e.id)).toEqual(["b", "c"]);
  });
});

// pev builds a progress event with overridable project/task/phase/kind for the collapse tests.
function pev(id: string, over: Partial<Event> = {}): Event {
  return {
    id,
    ts: "2026-01-01T00:00:00Z",
    project: "p",
    task: "t",
    phase: "develop",
    kind: "progress",
    payload: {},
    ...over,
  };
}

describe("collapseProgress", () => {
  it("folds consecutive same-(project,task,phase) progress pulses into one row + count", () => {
    const rows = collapseProgress([pev("p1"), pev("p2"), pev("p3")]);
    expect(rows).toHaveLength(1);
    // The first in input order is kept (the view feeds it newest-first, so the newest wins).
    expect(rows[0]?.event.id).toBe("p1");
    expect(rows[0]?.collapsed).toBe(2);
  });

  it("breaks the run on a non-progress event or a different phase", () => {
    const rows = collapseProgress([
      pev("a"),
      pev("b"),
      pev("d", { kind: "diff" }),
      pev("c"),
      pev("v1", { phase: "verify" }),
      pev("v2", { phase: "verify" }),
    ]);
    expect(rows.map((r) => [r.event.id, r.collapsed])).toEqual([
      ["a", 1],
      ["d", 0],
      ["c", 0],
      ["v1", 1],
    ]);
  });

  it("never folds progress pulses of different tasks together", () => {
    const rows = collapseProgress([pev("t1", { task: "T-1" }), pev("t2", { task: "T-2" })]);
    expect(rows).toHaveLength(2);
    expect(rows.every((r) => r.collapsed === 0)).toBe(true);
  });
});
