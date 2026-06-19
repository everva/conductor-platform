// Event-seam test (4A-1): prove useEventStream is transport-agnostic. We inject a
// FAKE EventTransport (no real socket, no token), capture the callbacks it is
// handed, and drive them: onOpen/onEvent/onClose must move state/events/latest,
// and unmount must call the subscription's close(). This is the proof that the
// same components can later run over the fork postMessage bridge.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import {
  useEventStream,
  type EventSubscription,
  type EventTransport,
} from "./useEventStream.ts";
import type { Event } from "./types.ts";

afterEach(() => {
  vi.restoreAllMocks();
});

// Captured callbacks from the most recent subscribe(), so the test can drive them.
interface Captured {
  filter?: unknown;
  onOpen: () => void;
  onEvent: (e: Event) => void;
  onClose: () => void;
}

// FakeEventTransport records subscribe args + reports whether close() was called.
class FakeEventTransport implements EventTransport {
  captured: Captured | null = null;
  closed = false;

  subscribe(opts: {
    filter?: import("./types.ts").EventQuery;
    onOpen: () => void;
    onEvent: (e: Event) => void;
    onClose: () => void;
  }): EventSubscription {
    this.captured = opts;
    return {
      close: () => {
        this.closed = true;
      },
    };
  }
}

const sample: Event = {
  id: "e1",
  ts: "2026-06-18T00:00:00Z",
  project: "proj",
  task: "t1",
  phase: "develop",
  kind: "progress",
  payload: { pct: 42 },
};

function Harness({
  transport,
  onState,
}: {
  transport: EventTransport;
  onState: (s: ReturnType<typeof useEventStream>) => void;
}) {
  const stream = useEventStream({
    // token is non-empty so the enabled gate passes; the fake transport ignores it.
    token: "ignored-by-fake",
    filter: { project: "proj" },
    transport,
  });
  onState(stream);
  return null;
}

describe("useEventStream with an injected EventTransport", () => {
  it("drives the fake transport's callbacks into state/events/latest", () => {
    const fake = new FakeEventTransport();
    let latestStream: ReturnType<typeof useEventStream> | null = null;
    render(<Harness transport={fake} onState={(s) => (latestStream = s)} />);

    // The hook subscribed through the injected transport (no real WebSocket).
    expect(fake.captured).not.toBeNull();
    expect(fake.captured!.filter).toEqual({ project: "proj" });
    expect(latestStream!.state).toBe("connecting");

    act(() => {
      fake.captured!.onOpen();
    });
    expect(latestStream!.state).toBe("open");

    act(() => {
      fake.captured!.onEvent(sample);
    });
    expect(latestStream!.latest).toEqual(sample);
    expect(latestStream!.events).toHaveLength(1);
    expect(latestStream!.events[0]).toEqual(sample);

    act(() => {
      fake.captured!.onClose();
    });
    expect(latestStream!.state).toBe("closed");
  });

  it("invokes the subscription's close() on unmount", () => {
    const fake = new FakeEventTransport();
    const { unmount } = render(<Harness transport={fake} onState={() => {}} />);

    expect(fake.closed).toBe(false);
    unmount();
    expect(fake.closed).toBe(true);
  });
});
