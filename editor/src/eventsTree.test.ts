// Deterministic, headless unit tests for the native events tree (Faz-Q / Q2). Uses the vscode
// mock; drives the provider with a stub EventsWatcher so the rendering is asserted without a
// network. Token-free by construction (the provider only ever sees FeedEvents).
import { describe, expect, it } from "vitest";
import { __reset } from "../test/vscode-mock";
import { EventsTreeProvider, eventKindPresentation, eventShortTime } from "./eventsTree";
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

  it("getTreeItem renders 'phase / kind', a 'project·task · time' detail, an icon, and contextValue", () => {
    const provider = new EventsTreeProvider(stubWatcher([]));
    const item = provider.getTreeItem(
      ev({ id: "e3", kind: "decision", phase: "review", project: "web-shop", task: "T-2", ts: "2026-06-22T10:05:09Z" }),
    );
    expect(item.label).toBe("review / decision");
    expect(item.description).toBe("web-shop·T-2 · 10:05:09");
    expect((item.iconPath as { id: string }).id).toBe("shield");
    expect(item.contextValue).toBe("conductorEvent");
  });

  it("falls back to just the kind when there is no phase, and drops the task when absent", () => {
    const provider = new EventsTreeProvider(stubWatcher([]));
    const item = provider.getTreeItem(ev({ id: "e4", kind: "health", phase: "", task: "" }));
    expect(item.label).toBe("health");
    expect(item.description).toBe("web-shop · 10:05:00");
  });
});
