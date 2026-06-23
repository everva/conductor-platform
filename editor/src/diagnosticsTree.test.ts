// Headless tests for the native Diagnostics tree (vscode aliased to the mock).
import { describe, expect, it, vi } from "vitest";

import { DiagnosticsTreeProvider, diagLevelIcon } from "./diagnosticsTree";
import type { DiagRow } from "./diagnostics";

const ROWS: DiagRow[] = [
  { label: "Connection", value: "connected", level: "ok" },
  { label: "Claude login (gateway)", value: "absent — run “Push Login to Gateway”", level: "warn" },
];

describe("diagLevelIcon", () => {
  it("maps each level to a codicon (ok/warn/bad colored, info plain)", () => {
    expect(diagLevelIcon("ok")).toMatchObject({ icon: "pass-filled", color: "charts.green" });
    expect(diagLevelIcon("warn")).toMatchObject({ icon: "warning", color: "charts.yellow" });
    expect(diagLevelIcon("bad")).toMatchObject({ icon: "error", color: "charts.red" });
    expect(diagLevelIcon("info")).toEqual({ icon: "info" });
  });
});

describe("DiagnosticsTreeProvider", () => {
  it("getChildren(root) returns the gathered rows; rows are leaves", async () => {
    const gather = vi.fn(() => Promise.resolve(ROWS));
    const p = new DiagnosticsTreeProvider(gather);
    const rootRows = await p.getChildren();
    expect(rootRows).toEqual(ROWS);
    expect(await p.getChildren(ROWS[0])).toEqual([]); // leaf
    expect(gather).toHaveBeenCalledTimes(1);
  });

  it("getTreeItem renders label + value (description) + a level icon", () => {
    const p = new DiagnosticsTreeProvider(() => Promise.resolve(ROWS));
    const item = p.getTreeItem(ROWS[1]!);
    expect(item.label).toBe("Claude login (gateway)");
    expect(item.description).toContain("Push Login to Gateway");
    expect(item.iconPath).toBeDefined();
  });

  it("refresh fires onDidChangeTreeData", () => {
    const p = new DiagnosticsTreeProvider(() => Promise.resolve(ROWS));
    const seen = vi.fn();
    p.onDidChangeTreeData(seen);
    p.refresh();
    expect(seen).toHaveBeenCalled();
  });
});
