// Headless tests for the native Activity tree (vscode aliased to the mock).
import { describe, expect, it, vi } from "vitest";

import { ActivityTreeProvider, activityLevelIcon } from "./activityTree";
import type { ActivityRow } from "./activity";

const ROWS: ActivityRow[] = [
  { project: "optiway", task: "VARDIYE-2", text: "Geliştiriyor · 4dk · 2 dosya", level: "active", ts: "t1" },
  { project: "optiway", task: "T-9", text: "Onay bekliyor", level: "attention", ts: "t2" },
];

describe("activityLevelIcon", () => {
  it("maps each level (active spins, attention bell, done check)", () => {
    expect(activityLevelIcon("active")).toMatchObject({ icon: "loading~spin" });
    expect(activityLevelIcon("attention")).toMatchObject({ icon: "bell", color: "charts.yellow" });
    expect(activityLevelIcon("done")).toMatchObject({ icon: "pass-filled" });
    expect(activityLevelIcon("idle")).toEqual({ icon: "circle-outline" });
  });
});

describe("ActivityTreeProvider", () => {
  it("getChildren(root) returns the rows; rows are leaves", () => {
    const p = new ActivityTreeProvider(() => ROWS);
    expect(p.getChildren()).toEqual(ROWS);
    expect(p.getChildren(ROWS[0])).toEqual([]);
  });

  it("getTreeItem renders task + text + a deep-link command", () => {
    const p = new ActivityTreeProvider(() => ROWS);
    const item = p.getTreeItem(ROWS[1]!);
    expect(item.label).toBe("T-9");
    expect(item.description).toBe("Onay bekliyor");
    expect(item.command).toMatchObject({ command: "conductor.openSession", arguments: ["optiway", "T-9"] });
  });

  it("refresh fires onDidChangeTreeData", () => {
    const p = new ActivityTreeProvider(() => ROWS);
    const seen = vi.fn();
    p.onDidChangeTreeData(seen);
    p.refresh();
    expect(seen).toHaveBeenCalled();
  });
});
