// Deterministic, headless tests for the host-side bridge (4B-2). Everything is faked —
// no real network, no `vscode`, no `ws` — so these run offline. They lock the two HARD
// security contracts:
//   - SSRF PATH GUARD: a path that isn't exactly one leading "/" is rejected with a
//     rest-error and NO fetch (the webview can't retarget the authed request).
//   - TOKEN ISOLATION: the sentinel token rides only in the fetch Authorization header +
//     the WS URL; the leak guard asserts it appears in NONE of the posted messages.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HostBridge, type TokenProvider, type WebviewLike, type WsConnector, type WsHandle } from "./hostBridge";

const SENTINEL = "sk-sentinel-do-not-leak-1234567890";

/** A fake webview: records every posted message and lets the test fire an inbound one.
 * The returned disposable actually unregisters the listener (so a test can assert that
 * dispose() stops delivery), mirroring the real vscode onDidReceiveMessage contract. */
function makeFakeWebview(): WebviewLike & {
  posted: unknown[];
  fire(message: unknown): void;
} {
  let listener: ((message: unknown) => void) | undefined;
  const posted: unknown[] = [];
  return {
    posted,
    postMessage(message: unknown) {
      posted.push(message);
    },
    onDidReceiveMessage(l: (message: unknown) => void) {
      listener = l;
      return {
        dispose: () => {
          if (listener === l) {
            listener = undefined;
          }
        },
      };
    },
    fire(message: unknown) {
      listener?.(message);
    },
  };
}

/** A TokenProvider returning a fixed token (or undefined for the not-connected cases). */
function makeTokenProvider(token: string | undefined): TokenProvider {
  return { getToken: () => Promise.resolve(token) };
}

/** Handler shape a WsConnector.open receives. */
type WsHandlers = { onOpen(): void; onMessage(data: string): void; onClose(): void };

/** A fake WS connector: records the URL + handlers and exposes a spyable close. */
function makeFakeWsConnector(): WsConnector & {
  opened: { url: string; handlers: WsHandlers }[];
  closes: ReturnType<typeof vi.fn>[];
} {
  const opened: { url: string; handlers: WsHandlers }[] = [];
  const closes: ReturnType<typeof vi.fn>[] = [];
  return {
    opened,
    closes,
    open(url: string, handlers: WsHandlers): WsHandle {
      opened.push({ url, handlers });
      const close = vi.fn();
      closes.push(close);
      return { close };
    },
  };
}

/** Flushes pending async work so the bridge's #handle settles before assertions. The
 * REST path chains getToken → fetch → res.text() (the last reads a ReadableStream, which
 * can span a macrotask), so yield a macrotask plus several microtasks. */
async function flush(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0));
  for (let i = 0; i < 4; i++) {
    await Promise.resolve();
  }
}

let webview: ReturnType<typeof makeFakeWebview>;
let ws: ReturnType<typeof makeFakeWsConnector>;

beforeEach(() => {
  webview = makeFakeWebview();
  ws = makeFakeWsConnector();
});

/** Builds a bridge with sensible defaults; overrides let a test swap the token/fetch. */
function makeBridge(opts: {
  token?: string | undefined;
  fetchImpl?: typeof fetch;
}): HostBridge {
  const bridge = new HostBridge({
    webview,
    baseUrl: "http://gw.test",
    wsBaseUrl: "ws://gw.test",
    tokenProvider: makeTokenProvider("token" in opts ? opts.token : SENTINEL),
    wsConnector: ws,
    ...(opts.fetchImpl ? { fetchImpl: opts.fetchImpl } : {}),
  });
  bridge.attach();
  return bridge;
}

describe("HostBridge — REST", () => {
  it("happy path: fetches baseUrl+path with Authorization: Bearer <token> and posts rest-response", async () => {
    const fetchImpl = vi
      .fn<(url: string, init?: RequestInit) => Promise<Response>>()
      .mockImplementation(() => Promise.resolve(new Response("pong", { status: 200 })));
    makeBridge({ fetchImpl: fetchImpl as unknown as typeof fetch });

    webview.fire({ kind: "rest-request", id: "b1", method: "GET", path: "/status" });
    await flush();

    expect(fetchImpl).toHaveBeenCalledTimes(1);
    const [url, init] = fetchImpl.mock.calls[0];
    expect(url).toBe("http://gw.test/status");
    expect((init?.headers as Record<string, string>).Authorization).toBe(`Bearer ${SENTINEL}`);
    expect(webview.posted).toEqual([
      { kind: "rest-response", id: "b1", status: 200, ok: true, body: "pong" },
    ]);
  });

  it("sends a body + Content-Type only when a body is present", async () => {
    const fetchImpl = vi
      .fn<(url: string, init?: RequestInit) => Promise<Response>>()
      .mockImplementation(() => Promise.resolve(new Response("{}", { status: 201 })));
    makeBridge({ fetchImpl: fetchImpl as unknown as typeof fetch });

    webview.fire({
      kind: "rest-request",
      id: "b1",
      method: "POST",
      path: "/projects",
      body: '{"repo":"x"}',
      contentType: "application/json",
    });
    await flush();

    const [, init] = fetchImpl.mock.calls[0];
    expect(init?.body).toBe('{"repo":"x"}');
    expect((init?.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });

  it.each([["//evil"], ["http://evil"], ["https://evil"], ["no-leading-slash"]])(
    "rejects unsafe path %s with rest-error and does NOT fetch (SSRF guard)",
    async (path) => {
      const fetchImpl = vi.fn(async () => new Response("", { status: 200 }));
      makeBridge({ fetchImpl: fetchImpl as unknown as typeof fetch });

      webview.fire({ kind: "rest-request", id: "b1", method: "GET", path });
      await flush();

      expect(fetchImpl).not.toHaveBeenCalled();
      expect(webview.posted).toEqual([{ kind: "rest-error", id: "b1", message: "invalid path" }]);
    },
  );

  it("posts a GENERIC rest-error (no token, no raw error) when fetch throws", async () => {
    const fetchImpl = vi.fn(async () => {
      throw new Error(`ECONNREFUSED http://gw.test/status?token=${SENTINEL}`);
    });
    makeBridge({ fetchImpl: fetchImpl as unknown as typeof fetch });

    webview.fire({ kind: "rest-request", id: "b1", method: "GET", path: "/status" });
    await flush();

    expect(webview.posted).toEqual([
      { kind: "rest-error", id: "b1", message: "gateway request failed" },
    ]);
  });

  it("posts a rest-error ('not connected') and does NOT fetch when there is no token", async () => {
    const fetchImpl = vi.fn(async () => new Response("", { status: 200 }));
    makeBridge({ token: undefined, fetchImpl: fetchImpl as unknown as typeof fetch });

    webview.fire({ kind: "rest-request", id: "b1", method: "GET", path: "/status" });
    await flush();

    expect(fetchImpl).not.toHaveBeenCalled();
    expect(webview.posted).toEqual([{ kind: "rest-error", id: "b1", message: "not connected" }]);
  });

  it("ignores messages that are not well-formed WebviewRequests", async () => {
    const fetchImpl = vi.fn(async () => new Response("", { status: 200 }));
    makeBridge({ fetchImpl: fetchImpl as unknown as typeof fetch });

    webview.fire({ kind: "garbage" });
    webview.fire(null);
    webview.fire({ kind: "rest-request", id: "b1" }); // missing method/path
    await flush();

    expect(fetchImpl).not.toHaveBeenCalled();
    expect(webview.posted).toEqual([]);
  });
});

describe("HostBridge — events", () => {
  it("opens a WS with the token query + filter and bridges open/message/close to posts", async () => {
    makeBridge({});

    webview.fire({
      kind: "event-subscribe",
      id: "e1",
      filter: { project: "proj", intervention: true },
    });
    await flush();

    expect(ws.opened).toHaveLength(1);
    const { url, handlers } = ws.opened[0];
    // Filter shape mirrors the web wsUrl: /ws?project=proj&intervention=1&token=<enc>.
    expect(url).toContain("ws://gw.test/ws?");
    expect(url).toContain("project=proj");
    expect(url).toContain("intervention=1");
    expect(url).toContain(`token=${encodeURIComponent(SENTINEL)}`);

    handlers.onOpen();
    handlers.onMessage('{"id":"x"}');
    handlers.onClose();

    expect(webview.posted).toEqual([
      { kind: "event-open", id: "e1" },
      { kind: "event-message", id: "e1", data: '{"id":"x"}' },
      { kind: "event-close", id: "e1" },
    ]);
  });

  it("uses '?' as the token separator when there is no filter", async () => {
    makeBridge({});
    webview.fire({ kind: "event-subscribe", id: "e1" });
    await flush();
    expect(ws.opened[0].url).toBe(`ws://gw.test/ws?token=${encodeURIComponent(SENTINEL)}`);
  });

  it("posts event-close immediately (no socket) when there is no token", async () => {
    makeBridge({ token: undefined });
    webview.fire({ kind: "event-subscribe", id: "e1" });
    await flush();
    expect(ws.opened).toHaveLength(0);
    expect(webview.posted).toEqual([{ kind: "event-close", id: "e1" }]);
  });

  it("event-unsubscribe closes the handle for that id", async () => {
    makeBridge({});
    webview.fire({ kind: "event-subscribe", id: "e1" });
    await flush();
    webview.fire({ kind: "event-unsubscribe", id: "e1" });
    await flush();
    expect(ws.closes[0]).toHaveBeenCalledTimes(1);
  });

  it("dispose() closes ALL open handles and the message listener", async () => {
    const bridge = makeBridge({});
    webview.fire({ kind: "event-subscribe", id: "e1" });
    webview.fire({ kind: "event-subscribe", id: "e2" });
    await flush();
    expect(ws.opened).toHaveLength(2);

    bridge.dispose();
    expect(ws.closes[0]).toHaveBeenCalledTimes(1);
    expect(ws.closes[1]).toHaveBeenCalledTimes(1);

    // After dispose the listener is gone: a further message drives nothing.
    webview.posted.length = 0;
    webview.fire({ kind: "event-subscribe", id: "e3" });
    await flush();
    expect(ws.opened).toHaveLength(2);
  });
});

describe("HostBridge — token-leak guard", () => {
  it("the sentinel token appears in NONE of the posted messages across all flows", async () => {
    // The gateway's response body carries NO token (the gateway never echoes auth), so a
    // clean invariant holds: the sentinel must appear in zero posted messages. This proves
    // the host attaches the token only to the fetch header + the WS URL (asserted in the
    // dedicated tests above) and never copies it into anything bound for the webview —
    // including the error path, where we post a generic message instead of the raw error.
    const fetchImpl = vi.fn(async () => new Response('{"ok":true}', { status: 200 }));
    const bridge = makeBridge({ fetchImpl: fetchImpl as unknown as typeof fetch });

    // Drive every flow: happy REST, bad-path REST, a throwing REST, event subscribe (with
    // frames), and unsubscribe. The throwing case proves the error message never echoes
    // the URL+token.
    const throwing = vi.fn(async () => {
      throw new Error(`ECONNREFUSED http://gw.test/status?token=${SENTINEL}`);
    });
    webview.fire({ kind: "rest-request", id: "b1", method: "GET", path: "/status" });
    webview.fire({ kind: "rest-request", id: "b2", method: "GET", path: "//evil" });
    webview.fire({ kind: "event-subscribe", id: "e1", filter: { project: "p" } });
    await flush();
    const { handlers } = ws.opened[0];
    handlers.onOpen();
    handlers.onMessage("frame-data");
    webview.fire({ kind: "event-unsubscribe", id: "e1" });
    await flush();

    // A second bridge whose fetch throws an error string CONTAINING the token — its posted
    // rest-error must still be the generic message, not the leaky raw error.
    const leaky = new HostBridge({
      webview,
      baseUrl: "http://gw.test",
      wsBaseUrl: "ws://gw.test",
      tokenProvider: makeTokenProvider(SENTINEL),
      wsConnector: ws,
      fetchImpl: throwing as unknown as typeof fetch,
    });
    leaky.attach();
    webview.fire({ kind: "rest-request", id: "b3", method: "GET", path: "/status" });
    await flush();
    bridge.dispose();
    leaky.dispose();

    // Serialize EVERY posted message and assert the sentinel is in none of them.
    expect(webview.posted.length).toBeGreaterThan(0);
    for (const msg of webview.posted) {
      expect(JSON.stringify(msg)).not.toContain(SENTINEL);
    }
  });
});
