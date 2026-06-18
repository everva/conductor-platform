// Unit tests for mergeById — the pure dedupe/sort/cap helper at the heart of the
// event feed. Stable ids + ts so assertions are deterministic.
import { describe, expect, it } from "vitest";
import { mergeById } from "./useEventFeed.ts";
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
