// Deterministic, headless unit tests for the host-side EventsWatcher (Faz-Q / Q2). Drives it
// with a FAKE WsConnector (records the opened URL + handlers, spyable close) + a fake fetch (for
// the REST backfill) + a fake TokenProvider — no real `ws`, no `vscode`, no network. The
// leak-guard proves the sentinel token rides ONLY in the WS URL + the backfill Authorization
// header and never reaches a FeedEvent / onChange / events().
import { describe, expect, it, vi } from "vitest";

import { EventsWatcher, toFeedEvent, type FeedEvent } from "./eventsWatcher";
import type { WsConnector, WsHandle } from "./bridge/hostBridge";

interface WsHandlers {
  onOpen(): void;
  onMessage(data: string): void;
  onClose(): void;
}

function makeFakeConnector(): WsConnector & {
  opens: { url: string; handlers: WsHandlers; handle: { close: ReturnType<typeof vi.fn> } }[];
  last(): { url: string; handlers: WsHandlers; handle: { close: ReturnType<typeof vi.fn> } };
} {
  const opens: { url: string; handlers: WsHandlers; handle: { close: ReturnType<typeof vi.fn> } }[] = [];
  return {
    open(url, handlers): WsHandle {
      const handle = { close: vi.fn() };
      opens.push({ url, handlers, handle });
      return handle;
    },
    opens,
    last() {
      const l = opens[opens.length - 1];
      if (l === undefined) throw new Error("no WS open recorded");
      return l;
    },
  };
}

const REST_BASE = "http://gw.test";
const WS_BASE = "ws://gw.test";
const SENTINEL = "tok-SENTINEL-MUST-NOT-LEAK";

// A fake fetch returning the given rows as the /events backfill (ok:true). Records calls so a
// test can assert the URL + the Authorization header (leak-guard). Typed as `typeof fetch` so it
// satisfies the watcher's `fetchImpl` seam.
function makeFetch(rows: unknown[]): ReturnType<typeof vi.fn<typeof fetch>> {
  return vi.fn<typeof fetch>(() =>
    Promise.resolve({ ok: true, json: () => Promise.resolve(rows) } as Response),
  );
}

function makeWatcher(
  token: string | undefined,
  rows: unknown[] = [],
  cap?: number,
): {
  watcher: EventsWatcher;
  connector: ReturnType<typeof makeFakeConnector>;
  fetchImpl: ReturnType<typeof makeFetch>;
  onChange: ReturnType<typeof vi.fn>;
} {
  const connector = makeFakeConnector();
  const fetchImpl = makeFetch(rows);
  const onChange = vi.fn();
  const watcher = new EventsWatcher({
    restBaseUrl: REST_BASE,
    wsBaseUrl: WS_BASE,
    tokenProvider: { getToken: () => Promise.resolve(token) },
    wsConnector: connector,
    onChange,
    fetchImpl,
    ...(cap !== undefined ? { cap } : {}),
  });
  return { watcher, connector, fetchImpl, onChange };
}

const evt = (over: Partial<FeedEvent> & { kind: string }): Record<string, unknown> => ({
  id: over.id ?? `id-${over.kind}`,
  ts: over.ts ?? "2026-06-22T10:00:00Z",
  project: over.project ?? "web-shop",
  task: over.task ?? "T-1",
  phase: over.phase ?? "review",
  kind: over.kind,
});

describe("toFeedEvent", () => {
  it("distills a full gateway event and tolerates extras", () => {
    expect(toFeedEvent({ id: "e1", ts: "t", project: "p", task: "T", phase: "review", kind: "diff", payload: {} })).toEqual(
      { id: "e1", ts: "t", project: "p", task: "T", phase: "review", kind: "diff" },
    );
  });
  it("rejects non-objects and a missing/empty kind", () => {
    expect(toFeedEvent(null)).toBeUndefined();
    expect(toFeedEvent("x")).toBeUndefined();
    expect(toFeedEvent({ project: "p" })).toBeUndefined(); // no kind
    expect(toFeedEvent({ kind: "" })).toBeUndefined(); // empty kind
  });
  it("synthesizes a stable id when absent and defaults missing fields to ''", () => {
    const e = toFeedEvent({ kind: "log", ts: "2026", project: "p", task: "T", phase: "develop" });
    expect(e?.id).toBe("2026:p:T:develop:log");
    // A bare event (only kind): id is synthesized from the (empty) parts; fields default to "".
    expect(toFeedEvent({ kind: "log" })).toEqual({
      id: "::::log",
      ts: "",
      project: "",
      task: "",
      phase: "",
      kind: "log",
    });
  });
});

describe("EventsWatcher", () => {
  it("no token → quiet no-op (no fetch, no WS, no change)", async () => {
    const { watcher, connector, fetchImpl, onChange } = makeWatcher(undefined);
    await watcher.start();
    expect(fetchImpl).not.toHaveBeenCalled();
    expect(connector.opens).toHaveLength(0);
    expect(onChange).not.toHaveBeenCalled();
    expect(watcher.events()).toHaveLength(0);
  });

  it("backfills (newest-first) then opens the live WS; onChange fires", async () => {
    const rows = [
      evt({ id: "a", ts: "2026-06-22T10:00:00Z", kind: "started" }),
      evt({ id: "b", ts: "2026-06-22T10:05:00Z", kind: "diff" }),
    ];
    const { watcher, connector, fetchImpl, onChange } = makeWatcher(SENTINEL, rows);
    await watcher.start();

    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect(connector.opens).toHaveLength(1);
    // Newest-first: the later ts (b) is at index 0.
    const ring = watcher.events();
    expect(ring.map((e) => e.id)).toEqual(["b", "a"]);
    expect(onChange).toHaveBeenCalled();
  });

  it("a live frame is prepended newest-first and deduped against the ring", async () => {
    const { watcher, connector } = makeWatcher(SENTINEL, [evt({ id: "a", ts: "2026-06-22T10:00:00Z", kind: "started" })]);
    await watcher.start();
    // A new live event.
    connector.last().handlers.onMessage(JSON.stringify(evt({ id: "c", ts: "2026-06-22T11:00:00Z", kind: "merge" })));
    expect(watcher.events().map((e) => e.id)).toEqual(["c", "a"]);
    // A duplicate id is ignored (no growth).
    connector.last().handlers.onMessage(JSON.stringify(evt({ id: "c", kind: "merge" })));
    expect(watcher.events().map((e) => e.id)).toEqual(["c", "a"]);
    // A malformed frame is ignored (no throw).
    expect(() => connector.last().handlers.onMessage("not json")).not.toThrow();
    expect(watcher.events()).toHaveLength(2);
  });

  it("caps the ring to the configured size (oldest dropped)", async () => {
    const rows = [
      evt({ id: "a", ts: "2026-06-22T10:00:00Z", kind: "log" }),
      evt({ id: "b", ts: "2026-06-22T10:01:00Z", kind: "log" }),
      evt({ id: "c", ts: "2026-06-22T10:02:00Z", kind: "log" }),
    ];
    const { watcher } = makeWatcher(SENTINEL, rows, 2);
    await watcher.start();
    // Cap 2, newest-first → c, b (a dropped).
    expect(watcher.events().map((e) => e.id)).toEqual(["c", "b"]);
  });

  it("LEAK GUARD: the token rides ONLY in the WS URL + the backfill Authorization header", async () => {
    const { watcher, connector, fetchImpl } = makeWatcher(SENTINEL, [evt({ id: "a", kind: "diff" })]);
    await watcher.start();
    connector.last().handlers.onMessage(JSON.stringify(evt({ id: "z", kind: "merge" })));

    // WS URL carries the token in the query (the ONLY WS egress).
    expect(connector.last().url).toContain(`token=${SENTINEL}`);
    // The backfill GET carried it in the Authorization header (the ONLY REST egress).
    const init = fetchImpl.mock.calls[0]?.[1];
    expect((init?.headers as Record<string, string>)?.Authorization).toBe(`Bearer ${SENTINEL}`);
    // It NEVER appears in any FeedEvent the watcher exposes.
    const dump = JSON.stringify(watcher.events());
    expect(dump).not.toContain(SENTINEL);
  });

  it("stop() closes the handle; restart closes the predecessor (no leak)", async () => {
    const { watcher, connector } = makeWatcher(SENTINEL);
    await watcher.start();
    const first = connector.last().handle;
    await watcher.start(); // restart
    expect(first.close).toHaveBeenCalledTimes(1);
    watcher.stop();
    expect(connector.last().handle.close).toHaveBeenCalledTimes(1);
  });
});
