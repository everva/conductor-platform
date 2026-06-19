// Deterministic, headless unit tests for the host-side intervention observer (4C-3).
// Drives InterventionNotifier with a FAKE WsConnector (records the opened URL + the
// handlers so a test can fire frames, and a spyable close) + a fake TokenProvider — no
// real `ws`, no `vscode`, no network. The notifier-leak guard proves the sentinel token
// rides ONLY in the WS URL and never reaches an `onIntervention` argument.
import { describe, expect, it, vi } from "vitest";

import { InterventionNotifier, type Intervention } from "./notifier";
import type { WsConnector, WsHandle } from "./bridge/hostBridge";

// The handler bundle the notifier hands to wsConnector.open (mirrors hostBridge's seam).
interface WsHandlers {
  onOpen(): void;
  onMessage(data: string): void;
  onClose(): void;
}

// A fake WsConnector that records every open() (url + handlers) and returns a handle with
// a spyable close. `last()` returns the most recent open so a test can fire frames at it.
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

// Builds an InterventionNotifier wired to a fresh fake connector + token provider, plus a
// spy onIntervention. Returns all three so a test asserts URL/handlers/callbacks.
function makeNotifier(token: string | undefined): {
  notifier: InterventionNotifier;
  connector: ReturnType<typeof makeFakeConnector>;
  onIntervention: ReturnType<typeof vi.fn<(i: Intervention) => void>>;
} {
  const connector = makeFakeConnector();
  const onIntervention = vi.fn<(i: Intervention) => void>();
  const notifier = new InterventionNotifier({
    wsBaseUrl: WS_BASE,
    tokenProvider: makeTokenProvider(token),
    wsConnector: connector,
    onIntervention,
  });
  return { notifier, connector, onIntervention };
}

// A gateway intervention-needed event frame (the wire shape: wsjson of events.Event).
function interventionFrame(over: Record<string, unknown> = {}): string {
  return JSON.stringify({
    project: "alpha",
    task: "t-1",
    kind: "intervention-needed",
    payload: { reason: "tests are red" },
    ts: "2026-06-19T00:00:00Z",
    ...over,
  });
}

describe("InterventionNotifier.start", () => {
  it("opens a WS filtered to intervention-needed with the token in the URL", async () => {
    const { notifier, connector } = makeNotifier(SENTINEL);

    await notifier.start();

    expect(connector.opens).toHaveLength(1);
    const { url } = connector.last();
    expect(url).toContain("kind=intervention-needed");
    expect(url).toContain(`token=${SENTINEL}`);
    // It extends the host's own ws base + /ws path (not some webview-chosen origin).
    expect(url.startsWith(`${WS_BASE}/ws?`)).toBe(true);
  });

  it("does NOT open a WS when there is no stored token (not connected)", async () => {
    const { notifier, connector } = makeNotifier(undefined);

    await notifier.start();

    expect(connector.opens).toHaveLength(0);
  });

  it("does NOT open a WS when the token is the empty string", async () => {
    const { notifier, connector } = makeNotifier("");

    await notifier.start();

    expect(connector.opens).toHaveLength(0);
  });

  it("closes the previous handle on a second start (no leaked socket)", async () => {
    const { notifier, connector } = makeNotifier(SENTINEL);

    await notifier.start();
    const first = connector.last().handle;
    await notifier.start();

    expect(connector.opens).toHaveLength(2);
    // The first socket was closed before/when the second opened — single active handle.
    expect(first.close).toHaveBeenCalledTimes(1);
    expect(connector.last().handle.close).not.toHaveBeenCalled();
  });
});

describe("InterventionNotifier frame handling", () => {
  it("calls onIntervention with the parsed {project,task,reason} for an intervention frame", async () => {
    const { notifier, connector, onIntervention } = makeNotifier(SENTINEL);
    await notifier.start();

    connector.last().handlers.onMessage(interventionFrame());

    expect(onIntervention).toHaveBeenCalledTimes(1);
    expect(onIntervention).toHaveBeenCalledWith({
      project: "alpha",
      task: "t-1",
      reason: "tests are red",
    });
  });

  it("falls back to a generic reason when payload.reason is absent", async () => {
    const { notifier, connector, onIntervention } = makeNotifier(SENTINEL);
    await notifier.start();

    connector.last().handlers.onMessage(interventionFrame({ payload: {} }));

    expect(onIntervention).toHaveBeenCalledTimes(1);
    const arg = onIntervention.mock.calls[0]?.[0];
    expect(arg?.project).toBe("alpha");
    expect(arg?.task).toBe("t-1");
    expect(arg?.reason.length).toBeGreaterThan(0);
    expect(arg?.reason).not.toBe(""); // a real human fallback, not empty.
  });

  it("ignores a non-intervention frame (no callback, no throw)", async () => {
    const { notifier, connector, onIntervention } = makeNotifier(SENTINEL);
    await notifier.start();

    expect(() =>
      connector.last().handlers.onMessage(
        JSON.stringify({ project: "alpha", task: "t-1", kind: "phase-changed", payload: {} }),
      ),
    ).not.toThrow();
    expect(onIntervention).not.toHaveBeenCalled();
  });

  it("ignores a malformed (non-JSON) frame (no callback, no throw)", async () => {
    const { notifier, connector, onIntervention } = makeNotifier(SENTINEL);
    await notifier.start();

    expect(() => connector.last().handlers.onMessage("not json {{{")).not.toThrow();
    expect(onIntervention).not.toHaveBeenCalled();
  });

  it("ignores an intervention frame missing string project/task", async () => {
    const { notifier, connector, onIntervention } = makeNotifier(SENTINEL);
    await notifier.start();

    // kind matches but project is not a string → defensively ignored.
    connector.last().handlers.onMessage(
      JSON.stringify({ project: 42, task: "t-1", kind: "intervention-needed", payload: {} }),
    );
    expect(onIntervention).not.toHaveBeenCalled();
  });
});

describe("InterventionNotifier.stop", () => {
  it("closes the active handle", async () => {
    const { notifier, connector } = makeNotifier(SENTINEL);
    await notifier.start();

    notifier.stop();

    expect(connector.last().handle.close).toHaveBeenCalledTimes(1);
  });

  it("is a safe no-op when never started", () => {
    const { notifier, connector } = makeNotifier(SENTINEL);

    expect(() => notifier.stop()).not.toThrow();
    expect(connector.opens).toHaveLength(0);
  });

  it("is idempotent (a second stop does not re-close / throw)", async () => {
    const { notifier, connector } = makeNotifier(SENTINEL);
    await notifier.start();

    notifier.stop();
    notifier.stop();

    expect(connector.last().handle.close).toHaveBeenCalledTimes(1);
  });
});

describe("InterventionNotifier token-leak guard", () => {
  it("the sentinel token appears ONLY in the WS URL, never in any onIntervention arg", async () => {
    const { notifier, connector, onIntervention } = makeNotifier(SENTINEL);
    await notifier.start();

    // Fire several frames (including one whose reason text is benign) through the captured
    // handler, then assert no onIntervention argument carries the token anywhere.
    connector.last().handlers.onMessage(interventionFrame());
    connector.last().handlers.onMessage(interventionFrame({ payload: {} }));
    connector.last().handlers.onMessage(interventionFrame({ project: "beta", task: "t-2" }));

    // The token is present in the URL (it must be — that's how the host authes the WS)…
    expect(connector.last().url).toContain(SENTINEL);
    // …but in NONE of the emitted interventions.
    for (const call of onIntervention.mock.calls) {
      expect(JSON.stringify(call[0])).not.toContain(SENTINEL);
    }
    expect(onIntervention).toHaveBeenCalledTimes(3);
  });
});
