// Deterministic, headless unit tests for the native events tree (Faz-Q / Q2). Uses the vscode
// mock; drives the provider with a stub EventsWatcher so the rendering is asserted without a
// network. Token-free by construction (the provider only ever sees FeedEvents).
import { describe, expect, it } from "vitest";
import { __reset } from "../test/vscode-mock";
import { EventsTreeProvider, eventKindPresentation, eventShortTime, eventContextValue, SHOW_EVENT_JSON_COMMAND } from "./eventsTree";
import type { EventsWatcher, FeedEvent } from "./eventsWatcher";

__reset();

// A stub watcher exposing a fixed ring (the provider only calls events()).
function stubWatcher(events: FeedEvent[]): EventsWatcher {
  return { events: () => events } as unknown as EventsWatcher;
}

const ev = (over: Partial<FeedEvent> & { id: string; kind: string }): FeedEvent => ({
  ts: "2026-06-22T10:05:00Z",
  project: "web-shop",
  task: "T-1",
  phase: "review",
  ...over,
});

describe("eventKindPresentation", () => {
  it("maps the frozen kinds to distinct codicons (grounded in events.gen.ts)", () => {
    expect(eventKindPresentation("intervention-needed").icon).toBe("bell");
    expect(eventKindPresentation("decision").icon).toBe("shield");
    expect(eventKindPresentation("diff").icon).toBe("git-compare");
    expect(eventKindPresentation("merge").icon).toBe("git-merge");
    expect(eventKindPresentation("started").icon).toBe("play-circle");
    // Unknown kinds get a neutral dot, not a throw.
    expect(eventKindPresentation("whatever").icon).toBe("circle-small-filled");
  });
});

describe("eventShortTime", () => {
  it("extracts HH:MM:SS from an ISO ts and falls back to the raw value", () => {
    expect(eventShortTime("2026-06-22T10:05:09Z")).toBe("10:05:09");
    expect(eventShortTime("nope")).toBe("nope");
  });
});

describe("EventsTreeProvider", () => {
  it("getChildren(root) yields the watcher's ring; events are leaves", () => {
    const provider = new EventsTreeProvider(stubWatcher([ev({ id: "a", kind: "diff" })]));
    expect(provider.getChildren()).toHaveLength(1);
    expect(provider.getChildren(ev({ id: "a", kind: "diff" }))).toEqual([]);
  });

  it("renders a readable STREAM line (time + plain text), task in the detail, icon, click→JSON", () => {
    const provider = new EventsTreeProvider(stubWatcher([]));
    const item = provider.getTreeItem(
      ev({ id: "e3", kind: "intervention-needed", phase: "review", project: "web-shop", task: "T-2", ts: "2026-06-22T10:05:09Z" }),
    );
    // Human line prefixed with the time — not "phase / kind" jargon.
    expect(item.label).toBe("10:05:09  Onay bekliyor");
    expect(item.description).toBe("web-shop·T-2"); // locator (time moved into the label)
    expect((item.iconPath as { id: string }).id).toBe("bell");
    // Click opens the full detailed JSON (drill-down).
    expect((item.command as { command: string }).command).toBe(SHOW_EVENT_JSON_COMMAND);
    // intervention-needed → the review action class.
    expect(item.contextValue).toBe("conductorEvent.review");
  });

  it("humanizes other kinds and drops the task from the detail when absent", () => {
    const provider = new EventsTreeProvider(stubWatcher([]));
    const item = provider.getTreeItem(ev({ id: "e4", kind: "merge", phase: "merge", task: "" }));
    expect(item.label).toBe("10:05:00  Birleştirildi ✓");
    expect(item.description).toBe("web-shop"); // no task
  });
});

describe("eventContextValue", () => {
  it("classifies actionable rows for the inline menus", () => {
    expect(eventContextValue(ev({ id: "1", kind: "intervention-needed" }))).toBe("conductorEvent.review");
    expect(eventContextValue(ev({ id: "2", kind: "decision", result: "blocked" }))).toBe("conductorEvent.blocked");
    expect(eventContextValue(ev({ id: "3", kind: "decision", result: "changes-requested" }))).toBe("conductorEvent.blocked");
    expect(eventContextValue(ev({ id: "4", kind: "diff" }))).toBe("conductorEvent.diff");
    expect(eventContextValue(ev({ id: "5", kind: "decision", result: "pass" }))).toBe("conductorEvent"); // passing decision: not actionable here
    expect(eventContextValue(ev({ id: "6", kind: "progress" }))).toBe("conductorEvent");
  });
});
