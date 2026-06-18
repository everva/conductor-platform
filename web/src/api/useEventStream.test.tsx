// useEventStream test: mock the global WebSocket, render the hook, dispatch a
// message frame, and assert the JSON is parsed into the Event buffer + latest.
// Fully offline; no real token, no real socket. Also asserts the URL carries the
// token + filter as query params (browser WS can't set headers).
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { useEventStream } from "./useEventStream.ts";
import type { Event } from "./types.ts";

// FakeWebSocket records the last instance + URL so the test can drive onmessage.
class FakeWebSocket {
  static last: FakeWebSocket | null = null;
  url: string;
  onopen: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.last = this;
  }

  close(): void {
    this.closed = true;
  }
}

afterEach(() => {
  vi.restoreAllMocks();
  FakeWebSocket.last = null;
});

const sample: Event = {
  id: "e1",
  ts: "2026-06-18T00:00:00Z",
  project: "proj",
  task: "t1",
  phase: "develop",
  kind: "progress",
  payload: { pct: 42 },
};

// Harness renders the hook and exposes its latest return via a ref.
function Harness({ onState }: { onState: (s: ReturnType<typeof useEventStream>) => void }) {
  const stream = useEventStream({
    wsBase: "ws://gw.test",
    token: "test-token",
    filter: { project: "proj" },
  });
  onState(stream);
  return null;
}

describe("useEventStream", () => {
  it("opens a WS with token + filter in the query and parses incoming messages", () => {
    vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);

    let latestStream: ReturnType<typeof useEventStream> | null = null;
    render(<Harness onState={(s) => (latestStream = s)} />);

    const ws = FakeWebSocket.last;
    expect(ws).not.toBeNull();
    expect(ws!.url).toContain("ws://gw.test/ws");
    expect(ws!.url).toContain("project=proj");
    expect(ws!.url).toContain("token=test-token");

    act(() => {
      ws!.onopen?.();
      ws!.onmessage?.({ data: JSON.stringify(sample) } as MessageEvent);
    });

    expect(latestStream).not.toBeNull();
    expect(latestStream!.state).toBe("open");
    expect(latestStream!.latest).toEqual(sample);
    expect(latestStream!.events).toHaveLength(1);
    expect(latestStream!.events[0]).toEqual(sample);
  });

  it("ignores malformed frames without crashing", () => {
    vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);

    let latestStream: ReturnType<typeof useEventStream> | null = null;
    render(<Harness onState={(s) => (latestStream = s)} />);

    act(() => {
      FakeWebSocket.last!.onmessage?.({ data: "not json{" } as MessageEvent);
    });

    expect(latestStream!.events).toHaveLength(0);
    expect(latestStream!.latest).toBeNull();
  });
});
