// Tests for the web event humanizer (PO audit — A1/A4). Pins each kind → plain-English line +
// severity level, and proves it reads the gateway's snake_case payload keys (the WS/REST shape),
// degrading safely on unknown/empty input. Pure: no React, no network.
import { describe, expect, it } from "vitest";
import { describeEvent } from "./eventText.ts";
import type { Event } from "../api/types.ts";

function ev(over: Partial<Event>): Event {
  return {
    id: "e",
    ts: "2026-06-24T00:00:00.000Z",
    project: "p1",
    task: "t1",
    phase: "develop",
    kind: "progress",
    payload: {},
    ...over,
  };
}

describe("describeEvent", () => {
  it("progress reads step + elapsed_seconds + files_changed (snake_case payload)", () => {
    expect(
      describeEvent(ev({ kind: "progress", payload: { step: "developing", elapsed_seconds: 125, files_changed: 3 } })),
    ).toEqual({ text: "Writing code · 2m · 3 files", level: "active" });
    // Singular file + sub-minute elapsed.
    expect(
      describeEvent(ev({ kind: "progress", payload: { step: "developing", elapsed_seconds: 5, files_changed: 1 } })),
    ).toEqual({ text: "Writing code · 5s · 1 file", level: "active" });
    // provisioning + verifying steps.
    expect(describeEvent(ev({ kind: "progress", payload: { step: "provisioning" } })).text).toBe(
      "Preparing workspace",
    );
    expect(describeEvent(ev({ kind: "progress", payload: { step: "verifying" } })).text).toBe("Running checks");
  });

  it("progress falls back to the coarse phase when no step is present", () => {
    expect(describeEvent(ev({ kind: "progress", phase: "plan", payload: {} })).text).toBe("Preparing workspace");
    expect(describeEvent(ev({ kind: "progress", phase: "verify", payload: {} })).text).toBe("Running checks");
  });

  it("decision: a blocked verdict surfaces the reason as attention", () => {
    expect(
      describeEvent(ev({ kind: "decision", payload: { result: "blocked", summary: "build failed: exit 1" } })),
    ).toEqual({ text: "Blocked: build failed: exit 1", level: "attention" });
    expect(describeEvent(ev({ kind: "decision", payload: { result: "changes-requested" } })).text).toBe("Blocked");
  });

  it("decision: a passing verdict reads as Awaiting approval (held-for-review)", () => {
    expect(describeEvent(ev({ kind: "decision", payload: { result: "passed" } }))).toEqual({
      text: "Awaiting approval",
      level: "attention",
    });
  });

  it("intervention-needed is the unmissable attention line", () => {
    expect(describeEvent(ev({ kind: "intervention-needed" }))).toEqual({
      text: "Needs your review",
      level: "attention",
    });
  });

  it("diff / merge / pr are concrete outcomes", () => {
    expect(describeEvent(ev({ kind: "diff", payload: { files_changed: 4 } })).text).toBe("Changes ready (4 files)");
    expect(describeEvent(ev({ kind: "diff", payload: {} })).text).toBe("Changes ready");
    expect(describeEvent(ev({ kind: "merge" }))).toEqual({ text: "Merged ✓", level: "done" });
    expect(describeEvent(ev({ kind: "pr" }))).toEqual({ text: "Pull request opened", level: "done" });
  });

  it("log / health surface the summary, truncated + whitespace-collapsed, else a label", () => {
    expect(describeEvent(ev({ kind: "log", payload: { summary: "hello\n  world" } })).text).toBe("hello world");
    expect(describeEvent(ev({ kind: "log", payload: {} })).text).toBe("Log");
    expect(describeEvent(ev({ kind: "health", payload: { summary: "ok" } }))).toEqual({
      text: "ok",
      level: "idle",
    });
    const long = "x".repeat(200);
    expect(describeEvent(ev({ kind: "log", payload: { summary: long } })).text.endsWith("…")).toBe(true);
    expect(describeEvent(ev({ kind: "log", payload: { summary: long } })).text.length).toBeLessThanOrEqual(120);
  });

  it("an unknown kind degrades to 'phase / kind', never crashing", () => {
    const e = ev({ kind: "mystery" as Event["kind"], phase: "review" });
    expect(describeEvent(e)).toEqual({ text: "review / mystery", level: "active" });
  });
});
