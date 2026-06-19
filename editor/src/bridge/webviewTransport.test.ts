// Deterministic, headless tests for the webview-side transports (4B-2). A fake poster
// records outbound WebviewRequests; a manually-driven listener feeds inbound HostMessages
// back. These lock id-correlated REST (resolve/reject, concurrent routing) and the event
// stream (open/message(parsed)/malformed-ignored/close + unsubscribe on close).
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createBridgeTransports,
  type HttpResponse,
  type SubscribeToMessages,
  type WebviewPoster,
} from "./webviewTransport";

/** A fake poster + a hand-driven inbound listener, paired so a test can post a host
 * reply for whatever the transport just sent. `subscribe` is the seam passed to
 * createBridgeTransports; `fire` injects an inbound HostMessage. */
function makeHarness(): {
  poster: WebviewPoster;
  posted: unknown[];
  subscribe: SubscribeToMessages;
  fire(message: unknown): void;
} {
  const posted: unknown[] = [];
  let listener: ((msg: unknown) => void) | undefined;
  return {
    poster: {
      postMessage(message: unknown) {
        posted.push(message);
      },
    },
    posted,
    subscribe(l: (msg: unknown) => void) {
      listener = l;
      return () => {
        listener = undefined;
      };
    },
    fire(message: unknown) {
      listener?.(message);
    },
  };
}

let harness: ReturnType<typeof makeHarness>;

beforeEach(() => {
  harness = makeHarness();
});

/** Builds the transports wired to the harness's poster + subscribe. */
function build(): ReturnType<typeof createBridgeTransports> {
  return createBridgeTransports(harness.poster, harness.subscribe);
}

describe("webviewTransport — dispose (review FAZ-4 MED: listener + pending cleanup)", () => {
  it("detaches the inbound listener and rejects in-flight REST on dispose", async () => {
    const { http, dispose } = build();
    const p = http.send({ method: "GET", path: "/status" }); // pending b1

    dispose();
    await expect(p).rejects.toThrow(/disposed/);

    // The inbound listener is detached: a late host reply for b1 is a quiet no-op (it must
    // not reach the router — the careful subscribeToMessages unsubscribe is now honored).
    expect(() =>
      harness.fire({ kind: "rest-response", id: "b1", status: 200, ok: true, body: "{}" }),
    ).not.toThrow();
  });
});

describe("webviewTransport — http.send", () => {
  it("posts a rest-request (monotonic id) and resolves with the matching rest-response", async () => {
    const { http } = build();
    const p = http.send({ method: "GET", path: "/status" });

    expect(harness.posted).toEqual([{ kind: "rest-request", id: "b1", method: "GET", path: "/status" }]);

    harness.fire({ kind: "rest-response", id: "b1", status: 200, ok: true, body: "pong" });
    const expected: HttpResponse = { status: 200, ok: true, body: "pong" };
    await expect(p).resolves.toEqual(expected);
  });

  it("includes body + contentType only when provided", () => {
    const { http } = build();
    void http.send({ method: "POST", path: "/x", body: "{}", contentType: "application/json" });
    expect(harness.posted[0]).toEqual({
      kind: "rest-request",
      id: "b1",
      method: "POST",
      path: "/x",
      body: "{}",
      contentType: "application/json",
    });
  });

  it("rejects with the host's message on rest-error", async () => {
    const { http } = build();
    const p = http.send({ method: "GET", path: "/status" });
    harness.fire({ kind: "rest-error", id: "b1", message: "not connected" });
    await expect(p).rejects.toThrow("not connected");
  });

  it("routes two concurrent sends to the right responses by id", async () => {
    const { http } = build();
    const p1 = http.send({ method: "GET", path: "/a" });
    const p2 = http.send({ method: "GET", path: "/b" });

    // Resolve out of order: b2 first, then b1.
    harness.fire({ kind: "rest-response", id: "b2", status: 201, ok: true, body: "B" });
    harness.fire({ kind: "rest-response", id: "b1", status: 200, ok: true, body: "A" });

    await expect(p1).resolves.toMatchObject({ body: "A", status: 200 });
    await expect(p2).resolves.toMatchObject({ body: "B", status: 201 });
  });

  it("ignores host messages for an unknown id and malformed inbound messages", async () => {
    const { http } = build();
    const p = http.send({ method: "GET", path: "/status" });
    harness.fire({ kind: "rest-response", id: "nope", status: 200, ok: true, body: "x" });
    harness.fire(null);
    harness.fire({ kind: "garbage" });
    // The real reply still resolves it.
    harness.fire({ kind: "rest-response", id: "b1", status: 200, ok: true, body: "ok" });
    await expect(p).resolves.toMatchObject({ body: "ok" });
  });
});

describe("webviewTransport — events.subscribe", () => {
  it("posts event-subscribe (with filter) and routes open/message(parsed)/close", () => {
    const { events } = build();
    const onOpen = vi.fn();
    const onEvent = vi.fn();
    const onClose = vi.fn();

    events.subscribe({ filter: { project: "p" }, onOpen, onEvent, onClose });
    expect(harness.posted).toEqual([{ kind: "event-subscribe", id: "b1", filter: { project: "p" } }]);

    harness.fire({ kind: "event-open", id: "b1" });
    expect(onOpen).toHaveBeenCalledTimes(1);

    harness.fire({ kind: "event-message", id: "b1", data: '{"id":"x","kind":"phase"}' });
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith({ id: "x", kind: "phase" }); // PARSED object

    harness.fire({ kind: "event-close", id: "b1" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("omits the filter key when no filter is given", () => {
    const { events } = build();
    events.subscribe({ onOpen: vi.fn(), onEvent: vi.fn(), onClose: vi.fn() });
    expect(harness.posted[0]).toEqual({ kind: "event-subscribe", id: "b1" });
  });

  it("ignores a malformed event-message (bad JSON) rather than crashing", () => {
    const { events } = build();
    const onEvent = vi.fn();
    events.subscribe({ onOpen: vi.fn(), onEvent, onClose: vi.fn() });
    harness.fire({ kind: "event-message", id: "b1", data: "not json{" });
    expect(onEvent).not.toHaveBeenCalled();
  });

  it("close() posts event-unsubscribe and drops the local subscription (late close is a no-op)", () => {
    const { events } = build();
    const onClose = vi.fn();
    const sub = events.subscribe({ onOpen: vi.fn(), onEvent: vi.fn(), onClose });

    sub.close();
    expect(harness.posted).toEqual([
      { kind: "event-subscribe", id: "b1" },
      { kind: "event-unsubscribe", id: "b1" },
    ]);

    // A host event-close arriving after the local close must NOT fire onClose again.
    harness.fire({ kind: "event-close", id: "b1" });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("gives each subscribe/send a fresh monotonic id", () => {
    const { http, events } = build();
    void http.send({ method: "GET", path: "/a" });
    events.subscribe({ onOpen: vi.fn(), onEvent: vi.fn(), onClose: vi.fn() });
    void http.send({ method: "GET", path: "/b" });
    expect((harness.posted[0] as { id: string }).id).toBe("b1");
    expect((harness.posted[1] as { id: string }).id).toBe("b2");
    expect((harness.posted[2] as { id: string }).id).toBe("b3");
  });
});
