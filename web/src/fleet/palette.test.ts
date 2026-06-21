// palette.ts tests (redesign E4). The model is pure, so we assert the contract
// directly: the always-present navigation/action rows, one "jump to session" row
// per task, AND-token filtering across id/project/lane/status, and that an
// open-session item carries the exact Task (so the component can drill in).
import { describe, expect, it } from "vitest";
import { buildPaletteItems } from "./palette.ts";
import type { Task } from "../api/types.ts";

function task(over: Partial<Task> = {}): Task {
  return {
    id: "T-1",
    project_id: "web-shop",
    lane: "backend",
    tier: "T1",
    status: "running",
    requires: [],
    deps: [],
    branch: "",
    scenario_id: "",
    retry_count: 0,
    abort_requested: false,
    approved: false,
    ...over,
  };
}

const TASKS: Record<string, Task[]> = {
  "web-shop": [task({ id: "WEB-1", project_id: "web-shop", lane: "backend", status: "running" })],
  "ios-app": [task({ id: "IOS-9", project_id: "ios-app", lane: "ios", status: "awaiting-approval" })],
};

describe("buildPaletteItems", () => {
  it("an empty query yields the static nav/action rows plus one session per task", () => {
    const items = buildPaletteItems(TASKS, "");
    const ids = items.map((i) => i.id);
    // Static rows present, in group order (Go to → Actions), before any session.
    expect(ids.slice(0, 4)).toEqual(["go:board", "go:fleet", "go:events", "act:new-work"]);
    // One session row per task.
    expect(ids).toContain("session:web-shop:WEB-1");
    expect(ids).toContain("session:ios-app:IOS-9");
    // Groups never interleave: every "Go to" precedes "Actions" precedes "Sessions".
    const groups = items.map((i) => i.group);
    expect(groups).toEqual([...groups].sort(
      (a, b) =>
        ["Go to", "Actions", "Sessions"].indexOf(a) -
        ["Go to", "Actions", "Sessions"].indexOf(b),
    ));
  });

  it("filters navigation rows by an AND match over the query tokens", () => {
    const items = buildPaletteItems(TASKS, "go events");
    expect(items.map((i) => i.id)).toEqual(["go:events"]);
  });

  it("matches a session by task id (case-insensitive)", () => {
    const items = buildPaletteItems(TASKS, "web-1");
    expect(items.map((i) => i.id)).toEqual(["session:web-shop:WEB-1"]);
  });

  it("matches sessions by lane and by status", () => {
    expect(buildPaletteItems(TASKS, "backend").map((i) => i.id)).toEqual([
      "session:web-shop:WEB-1",
    ]);
    expect(buildPaletteItems(TASKS, "awaiting").map((i) => i.id)).toEqual([
      "session:ios-app:IOS-9",
    ]);
  });

  it("AND-narrows across project and lane tokens", () => {
    // "ios" matches the ios-app session by project AND lane; "backend" would not.
    expect(buildPaletteItems(TASKS, "ios app").map((i) => i.id)).toEqual([
      "session:ios-app:IOS-9",
    ]);
    expect(buildPaletteItems(TASKS, "web backend").map((i) => i.id)).toEqual([
      "session:web-shop:WEB-1",
    ]);
  });

  it("an open-session item carries the exact Task for drill-in", () => {
    const item = buildPaletteItems(TASKS, "ios-9")[0];
    expect(item.action).toEqual({
      type: "open-session",
      task: TASKS["ios-app"][0],
    });
    expect(item.hint).toBe("ios-app · awaiting-approval");
  });

  it("a non-matching query yields no items", () => {
    expect(buildPaletteItems(TASKS, "zzz-nothing")).toEqual([]);
  });
});
