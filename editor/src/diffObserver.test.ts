// Deterministic, headless unit tests for the host-side diff observer (4C-1b). Drives
// DiffObserver with a FAKE WsConnector (records the opened URL + the handlers so a test can
// fire frames, and a spyable close) + a fake TokenProvider — no real `ws`, no `vscode`, no
// network. The diff-leak guard proves the sentinel token rides ONLY in the WS URL and never
// reaches an `onDiff` argument. Mirrors notifier.test.ts.
import { describe, expect, it, vi } from "vitest";

import { DiffObserver, DIFF_KIND, type TaskDiff } from "./diffObserver";
import type { WsConnector, WsHandle } from "./bridge/hostBridge";

// The handler bundle the observer hands to wsConnector.open (mirrors hostBridge's seam).
interface WsHandlers {
  onOpen(): void;
  onMessage(data: string): void;
  onClose(): void;
}

// A fake WsConnector that records every open() (url + handlers) and returns a handle with a
// spyable close. `last()` returns the most recent open so a test can fire frames at it.
function makeFakeConnector(): WsConnector & {
  opens: { url: string; handlers: WsHandlers; handle: WsHandle & { close: ReturnType<typeof vi.fn> } }[];
  last(): { url: string; handlers: WsHandlers; handle: WsHandle & { close: ReturnType<typeof vi.fn> } };
} {
  const opens: {
    url: string;
    handlers: WsHandlers;
    handle: WsHandle & { close: ReturnType<typeof vi.fn> };
  }[] = [];
  return {
    open(url, handlers): WsHandle {
      const handle = { close: vi.fn() };
      opens.push({ url, handlers, handle });
      return handle;
    },
    opens,
    last() {
      const l = opens[opens.length - 1];
      if (l === undefined) {
        throw new Error("no WS open recorded");
      }
      return l;
    },
  };
}

// A fake TokenProvider resolving a fixed value (or undefined for the not-connected case).
function makeTokenProvider(token: string | undefined): { getToken(): Promise<string | undefined> } {
  return { getToken: () => Promise.resolve(token) };
}

const WS_BASE = "ws://gw.test";
const SENTINEL = "tok-SENTINEL-MUST-NOT-LEAK";

// Builds a DiffObserver wired to a fresh fake connector + token provider, plus a spy onDiff.
// Returns all three so a test asserts URL/handlers/callbacks.
function makeObserver(token: string | undefined): {
  observer: DiffObserver;
  connector: ReturnType<typeof makeFakeConnector>;
  onDiff: ReturnType<typeof vi.fn<(d: TaskDiff) => void>>;
} {
  const connector = makeFakeConnector();
  const onDiff = vi.fn<(d: TaskDiff) => void>();
  const observer = new DiffObserver({
    wsBaseUrl: WS_BASE,
    tokenProvider: makeTokenProvider(token),
    wsConnector: connector,
    onDiff,
  });
  return { observer, connector, onDiff };
}

// A gateway diff event frame (the wire shape: wsjson of events.Event, payload =
// events.DiffSummary.Payload()).
function diffFrame(over: Record<string, unknown> = {}, payloadOver: Record<string, unknown> = {}): string {
  return JSON.stringify({
    project: "alpha",
    task: "t-1",
    phase: "review",
    kind: "diff",
    payload: {
      branch: "task/t-1",
      base: "main",
      files: [
        { path: "src/a.ts", status: "M", additions: 3, deletions: 1 },
        { path: "src/b.ts", status: "A", additions: 10, deletions: 0 },
      ],
      patch: "diff --git a/src/a.ts b/src/a.ts\n@@ -1 +1 @@\n-old\n+new\n",
      truncated: false,
      ...payloadOver,
    },
    ts: "2026-06-19T00:00:00Z",
    ...over,
  });
}

describe("DiffObserver.start", () => {
  it("opens a WS filtered to diff with the token in the URL", async () => {
    const { observer, connector } = makeObserver(SENTINEL);

    await observer.start();

    expect(connector.opens).toHaveLength(1);
    const { url } = connector.last();
    expect(url).toContain(`kind=${DIFF_KIND}`);
    expect(url).toContain(`token=${SENTINEL}`);
    // It extends the host's own ws base + /ws path (not some webview-chosen origin).
    expect(url.startsWith(`${WS_BASE}/ws?`)).toBe(true);
  });

  it("does NOT open a WS when there is no stored token (not connected)", async () => {
    const { observer, connector } = makeObserver(undefined);

    await observer.start();

    expect(connector.opens).toHaveLength(0);
  });

  it("does NOT open a WS when the token is the empty string", async () => {
    const { observer, connector } = makeObserver("");

    await observer.start();

    expect(connector.opens).toHaveLength(0);
  });

  it("closes the previous handle on a second start (no leaked socket)", async () => {
    const { observer, connector } = makeObserver(SENTINEL);

    await observer.start();
    const first = connector.last().handle;
    await observer.start();

    expect(connector.opens).toHaveLength(2);
    // The first socket was closed before/when the second opened — single active handle.
    expect(first.close).toHaveBeenCalledTimes(1);
    expect(connector.last().handle.close).not.toHaveBeenCalled();
  });
});

describe("DiffObserver frame handling", () => {
  it("calls onDiff with the distilled TaskDiff for a diff frame", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    connector.last().handlers.onMessage(diffFrame());

    expect(onDiff).toHaveBeenCalledTimes(1);
    expect(onDiff).toHaveBeenCalledWith({
      project: "alpha",
      task: "t-1",
      branch: "task/t-1",
      base: "main",
      files: [
        { path: "src/a.ts", status: "M", additions: 3, deletions: 1 },
        { path: "src/b.ts", status: "A", additions: 10, deletions: 0 },
      ],
      patch: "diff --git a/src/a.ts b/src/a.ts\n@@ -1 +1 @@\n-old\n+new\n",
      truncated: false,
    });
  });

  it("carries the truncated flag through when set", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    connector.last().handlers.onMessage(diffFrame({}, { truncated: true }));

    expect(onDiff.mock.calls[0]?.[0]?.truncated).toBe(true);
  });

  it("defaults a partial payload (missing fields) to safe empties", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    // A diff event whose payload is just {} (no branch/base/files/patch/truncated).
    connector.last().handlers.onMessage(
      JSON.stringify({ project: "alpha", task: "t-1", kind: "diff", payload: {} }),
    );

    expect(onDiff).toHaveBeenCalledTimes(1);
    expect(onDiff).toHaveBeenCalledWith({
      project: "alpha",
      task: "t-1",
      branch: "",
      base: "",
      files: [],
      patch: "",
      truncated: false,
    });
  });

  it("tolerates a missing payload entirely", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    connector.last().handlers.onMessage(
      JSON.stringify({ project: "alpha", task: "t-1", kind: "diff" }),
    );

    expect(onDiff).toHaveBeenCalledTimes(1);
    const arg = onDiff.mock.calls[0]?.[0];
    expect(arg?.files).toEqual([]);
    expect(arg?.patch).toBe("");
  });

  it("drops non-conforming file entries (no string path) but keeps valid ones", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    connector.last().handlers.onMessage(
      diffFrame({}, {
        files: [
          { path: "ok.ts", status: "M", additions: 1, deletions: 2 },
          { status: "A", additions: 5, deletions: 0 }, // no path → dropped
          "not-an-object", // non-object → dropped
          { path: 42 }, // non-string path → dropped
        ],
      }),
    );

    const files = onDiff.mock.calls[0]?.[0]?.files;
    expect(files).toEqual([{ path: "ok.ts", status: "M", additions: 1, deletions: 2 }]);
  });

  it("defaults a file row's missing status/counts (path-only row)", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    connector.last().handlers.onMessage(diffFrame({}, { files: [{ path: "p.ts" }] }));

    expect(onDiff.mock.calls[0]?.[0]?.files).toEqual([
      { path: "p.ts", status: "", additions: 0, deletions: 0 },
    ]);
  });

  it("ignores a non-diff frame (no callback, no throw)", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    expect(() =>
      connector.last().handlers.onMessage(
        JSON.stringify({ project: "alpha", task: "t-1", kind: "phase-changed", payload: {} }),
      ),
    ).not.toThrow();
    expect(onDiff).not.toHaveBeenCalled();
  });

  it("ignores a malformed (non-JSON) frame (no callback, no throw)", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    expect(() => connector.last().handlers.onMessage("not json {{{")).not.toThrow();
    expect(onDiff).not.toHaveBeenCalled();
  });

  it("ignores a diff frame missing string project/task", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    // kind matches but project is not a string → defensively ignored.
    connector.last().handlers.onMessage(
      JSON.stringify({ project: 42, task: "t-1", kind: "diff", payload: {} }),
    );
    expect(onDiff).not.toHaveBeenCalled();
  });
});

describe("DiffObserver.stop", () => {
  it("closes the active handle", async () => {
    const { observer, connector } = makeObserver(SENTINEL);
    await observer.start();

    observer.stop();

    expect(connector.last().handle.close).toHaveBeenCalledTimes(1);
  });

  it("is a safe no-op when never started", () => {
    const { observer, connector } = makeObserver(SENTINEL);

    expect(() => observer.stop()).not.toThrow();
    expect(connector.opens).toHaveLength(0);
  });

  it("is idempotent (a second stop does not re-close / throw)", async () => {
    const { observer, connector } = makeObserver(SENTINEL);
    await observer.start();

    observer.stop();
    observer.stop();

    expect(connector.last().handle.close).toHaveBeenCalledTimes(1);
  });
});

describe("DiffObserver token-leak guard", () => {
  it("the sentinel token appears ONLY in the WS URL, never in any onDiff arg", async () => {
    const { observer, connector, onDiff } = makeObserver(SENTINEL);
    await observer.start();

    // Fire several frames through the captured handler, then assert no onDiff argument
    // carries the token anywhere (project/task/branch/base/files/patch).
    connector.last().handlers.onMessage(diffFrame());
    connector.last().handlers.onMessage(diffFrame({}, { truncated: true }));
    connector.last().handlers.onMessage(diffFrame({ project: "beta", task: "t-2" }));

    // The token is present in the URL (it must be — that's how the host authes the WS)…
    expect(connector.last().url).toContain(SENTINEL);
    // …but in NONE of the emitted diffs.
    for (const call of onDiff.mock.calls) {
      expect(JSON.stringify(call[0])).not.toContain(SENTINEL);
    }
    expect(onDiff).toHaveBeenCalledTimes(3);
  });
});
