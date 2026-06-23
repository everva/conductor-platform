// Deterministic tests for the pure "Now"/Activity model. No vscode, no network.
import { describe, expect, it } from "vitest";

import { summarizeActivity, activityNowText } from "./activity";
import type { FeedEvent } from "./eventsWatcher";

let n = 0;
function ev(over: Partial<FeedEvent>): FeedEvent {
  n += 1;
  return {
    id: `e${n}`,
    ts: `2026-06-23T16:00:${String(n).padStart(2, "0")}Z`,
    project: "optiway",
    task: "VARDIYE-2",
    phase: "",
    kind: "progress",
    ...over,
  };
}

describe("summarizeActivity → plain lines", () => {
  it("developing with elapsed + files", () => {
    const rows = summarizeActivity([ev({ kind: "progress", phase: "developing", elapsedSeconds: 240, filesChanged: 2 })]);
    expect(rows[0]).toMatchObject({ task: "VARDIYE-2", level: "active" });
    expect(rows[0]!.text).toBe("Geliştiriyor · 4dk · 2 dosya");
  });

  it("verifying shows 'Test ediliyor'", () => {
    const rows = summarizeActivity([ev({ kind: "progress", phase: "verifying", elapsedSeconds: 45 })]);
    expect(rows[0]!.text).toBe("Test ediliyor · 45sn");
  });

  it("production shape: coarse valid phase + granular step in payload", () => {
    // The gateway validates phase∈{plan,develop,verify}; the precise step rides in `step`.
    expect(summarizeActivity([ev({ kind: "progress", phase: "develop", step: "developing", elapsedSeconds: 120, filesChanged: 1 })])[0]!.text).toBe(
      "Geliştiriyor · 2dk · 1 dosya",
    );
    expect(summarizeActivity([ev({ kind: "progress", phase: "plan", step: "provisioning" })])[0]!.text).toBe("Hazırlanıyor");
    expect(summarizeActivity([ev({ kind: "progress", phase: "verify", step: "verifying", elapsedSeconds: 300 })])[0]!.text).toBe("Test ediliyor · 5dk");
  });

  it("provisioning shows 'Hazırlanıyor'", () => {
    const rows = summarizeActivity([ev({ kind: "progress", phase: "provisioning" })]);
    expect(rows[0]!.text).toBe("Hazırlanıyor");
  });

  it("blocked decision shows the reason + attention", () => {
    const rows = summarizeActivity([
      ev({ kind: "decision", phase: "review", result: "blocked", summary: "agent run failed: provision workspace: cannot lock ref" }),
    ]);
    expect(rows[0]!.level).toBe("attention");
    expect(rows[0]!.text).toContain("Tıkandı:");
    expect(rows[0]!.text).toContain("cannot lock ref");
  });

  it("held / intervention-needed → 'Onay bekliyor' attention", () => {
    expect(summarizeActivity([ev({ kind: "intervention-needed", phase: "review" })])[0]).toMatchObject({
      text: "Onay bekliyor",
      level: "attention",
    });
    expect(summarizeActivity([ev({ kind: "decision", phase: "review", result: "pass" })])[0]!.text).toBe("Onay bekliyor");
  });

  it("merge → done", () => {
    expect(summarizeActivity([ev({ kind: "merge", phase: "merge" })])[0]).toMatchObject({
      text: "Birleştirildi ✓",
      level: "done",
    });
  });

  it("keeps only the LATEST event per task (events are newest-first)", () => {
    const rows = summarizeActivity([
      ev({ task: "A", kind: "progress", phase: "verifying", elapsedSeconds: 60 }), // newest for A
      ev({ task: "A", kind: "progress", phase: "developing" }), // older A — ignored
      ev({ task: "B", kind: "merge", phase: "merge" }),
    ]);
    expect(rows.map((r) => r.task)).toEqual(["A", "B"]);
    expect(rows[0]!.text).toBe("Test ediliyor · 1dk");
  });

  it("skips events without a task id", () => {
    expect(summarizeActivity([ev({ task: "", kind: "health", phase: "" })])).toHaveLength(0);
  });
});

describe("activityNowText (status-bar headline)", () => {
  it("empty rows → empty string", () => {
    expect(activityNowText([])).toBe("");
  });

  it("prefers a task needing attention over an active one", () => {
    const rows = summarizeActivity([
      ev({ task: "A", kind: "progress", phase: "developing" }),
      ev({ task: "B", kind: "decision", phase: "review", result: "blocked", summary: "boom" }),
    ]);
    const text = activityNowText(rows);
    expect(text).toContain("B:");
    expect(text).toContain("Tıkandı");
    expect(text).toContain("$(pulse)");
  });

  it("falls back to the most-recent active task", () => {
    const rows = summarizeActivity([ev({ task: "A", kind: "progress", phase: "developing", elapsedSeconds: 30 })]);
    expect(activityNowText(rows)).toBe("$(pulse) A: Geliştiriyor · 30sn");
  });
});
