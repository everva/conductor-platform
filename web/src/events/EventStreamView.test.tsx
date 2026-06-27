// EventStreamView tests (3B-2). The history surface is a fake HistoryLoader (no
// network) and the live WebSocket is a controllable FakeWebSocket so we can drive
// onopen/onmessage/onclose deterministically. We assert: (a) history backfill renders
// on mount; (b) a live event prepends and is DEDUPED against a same-id history row;
// (c) an intervention-needed event gets the highlight marker/class; (d) changing the
// project filter re-queries history with the new params AND re-subscribes the socket;
// (e) the buffer cap drops the oldest; (f) the pause toggle freezes display updates;
// and (g) unmount / filter change tears the previous socket down (no leak).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { EventStreamView } from "./EventStreamView.tsx";
import type { HistoryLoader } from "./useEventFeed.ts";
import type { Event, EventQuery } from "../api/types.ts";

// FakeWebSocket records every instance + tracks close() so leak/teardown is testable.
class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  url: string;
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }
  close(): void {
    this.closed = true;
  }
  static last(): FakeWebSocket {
    const ws = FakeWebSocket.instances[FakeWebSocket.instances.length - 1];
    if (!ws) {
      throw new Error("no WebSocket constructed");
    }
    return ws;
  }
}

// Defaults to a MILESTONE kind ("log") so generic rows are visible — heartbeats (kind
// "progress") are hidden by default in the view, so progress-specific tests opt in explicitly
// and toggle "Show heartbeats".
function ev(over: Partial<Event>): Event {
  return {
    id: "e",
    ts: "2026-06-18T00:00:00.000Z",
    project: "p1",
    task: "t1",
    phase: "develop",
    kind: "log",
    payload: {},
    ...over,
  };
}

// makeHistory builds a fake whose listEvents is a spy returning a queued result; the
// test inspects the spy's call args to assert filter-driven re-queries.
function makeFakeHistory(initial: Event[]): {
  loader: HistoryLoader;
  spy: ReturnType<typeof vi.fn>;
  setNext: (rows: Event[]) => void;
} {
  let next = initial;
  const spy = vi.fn(async (): Promise<Event[]> => next);
  return {
    loader: { listEvents: spy },
    spy,
    setNext: (rows) => {
      next = rows;
    },
  };
}

beforeEach(() => {
  vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
  FakeWebSocket.instances = [];
});

afterEach(() => {
  vi.restoreAllMocks();
});

// flush settles the backfill promise microtasks.
async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

function pushLive(e: Event): void {
  act(() => {
    FakeWebSocket.last().onopen?.();
    FakeWebSocket.last().onmessage?.({ data: JSON.stringify(e) } as MessageEvent);
  });
}

describe("EventStreamView", () => {
  it("(a) backfills history on mount", async () => {
    const h = makeFakeHistory([
      ev({ id: "h1", ts: "2026-06-18T00:00:01.000Z" }),
      ev({ id: "h2", ts: "2026-06-18T00:00:02.000Z", kind: "log" }),
    ]);
    render(<EventStreamView token="tkn" makeHistory={() => h.loader} />);
    await flush();

    expect(h.spy).toHaveBeenCalledTimes(1);
    expect(screen.getAllByTestId("evt-row")).toHaveLength(2);
    // newest-first: h2 (ts 02) is rendered before h1 (ts 01).
    const rows = screen.getAllByTestId("evt-row");
    expect(within(rows[0]).getByText("log")).toBeInTheDocument();
  });

  it("(b) appends a live event and dedupes a same-id history row", async () => {
    const shared = ev({ id: "dup", ts: "2026-06-18T00:00:05.000Z", kind: "decision" });
    const h = makeFakeHistory([shared]);
    render(<EventStreamView token="tkn" makeHistory={() => h.loader} />);
    await flush();
    expect(screen.getAllByTestId("evt-row")).toHaveLength(1);

    // Live: a brand-new event AND a re-delivery of the same-id history event.
    pushLive(ev({ id: "live1", ts: "2026-06-18T00:00:06.000Z", kind: "pr" }));
    await flush();
    pushLive(shared); // same id as history — must not duplicate.
    await flush();

    const rows = screen.getAllByTestId("evt-row");
    expect(rows).toHaveLength(2); // dup deduped; only live1 added.
    // Newest first: live1 (ts 06) at top.
    expect(within(rows[0]).getByText("pr")).toBeInTheDocument();
  });

  it("(c) highlights an intervention-needed event", async () => {
    const h = makeFakeHistory([
      ev({ id: "i1", kind: "intervention-needed", phase: "review" }),
    ]);
    render(<EventStreamView token="tkn" makeHistory={() => h.loader} />);
    await flush();

    const row = screen.getByTestId("evt-row");
    expect(row.className).toContain("intervention");
    expect(within(row).getByLabelText(/intervention needed/i)).toBeInTheDocument();
  });

  it("(c2) an intervention row exposes a contextual Resolve action (3B-3)", async () => {
    const h = makeFakeHistory([
      ev({ id: "i1", project: "p7", task: "t7", kind: "intervention-needed", phase: "review" }),
      ev({ id: "n1", project: "p7", kind: "log" }),
    ]);
    const onInterventionAction = vi.fn();
    render(
      <EventStreamView
        token="tkn"
        makeHistory={() => h.loader}
        onInterventionAction={onInterventionAction}
      />,
    );
    await flush();

    // Only the intervention row carries the Resolve affordance.
    const buttons = screen.getAllByRole("button", { name: /^resolve$/i });
    expect(buttons).toHaveLength(1);
    await act(async () => {
      buttons[0].click();
    });
    expect(onInterventionAction).toHaveBeenCalledWith("p7", "t7");
  });

  it("(a2) renders a human-readable line, not raw JSON, and expands to the full payload on click", async () => {
    const h = makeFakeHistory([
      ev({
        id: "h1",
        kind: "decision",
        phase: "verify",
        payload: { result: "blocked", summary: "build failed: exit 1", checks: [{ name: "gate" }] },
      }),
    ]);
    render(<EventStreamView token="tkn" makeHistory={() => h.loader} />);
    await flush();

    const row = screen.getByTestId("evt-row");
    // A1: the row shows the plain-English line, NOT the raw JSON dump.
    expect(within(row).getByText("Blocked: build failed: exit 1")).toBeInTheDocument();
    expect(within(row).queryByText(/"result":"blocked"/)).not.toBeInTheDocument();

    // A2: collapsed by default — no JSON detail yet.
    expect(within(row).queryByTestId("evt-json")).not.toBeInTheDocument();
    const disclosure = within(row).getByRole("button");
    expect(disclosure).toHaveAttribute("aria-expanded", "false");

    // Click → the full, pretty-printed payload appears (newlines = formatted, not the 160-char dump).
    await act(async () => {
      disclosure.click();
    });
    const json = within(row).getByTestId("evt-json");
    expect(json.textContent).toContain('"result": "blocked"');
    expect(json.textContent).toContain('"summary": "build failed: exit 1"');
    expect(json.textContent).toContain("\n"); // pretty-printed
    expect(disclosure).toHaveAttribute("aria-expanded", "true");

    // Click again → collapses.
    await act(async () => {
      disclosure.click();
    });
    expect(within(row).queryByTestId("evt-json")).not.toBeInTheDocument();
  });

  it("(d) re-queries history with the new project filter and updates", async () => {
    const h = makeFakeHistory([ev({ id: "h1", project: "p1" })]);
    render(
      <EventStreamView
        token="tkn"
        projects={["p1", "p2"]}
        makeHistory={() => h.loader}
      />,
    );
    await flush();
    expect(h.spy).toHaveBeenCalledTimes(1);

    // New filter → new result set.
    h.setNext([ev({ id: "h2", project: "p2", kind: "log" })]);
    await act(async () => {
      fireEvent.change(screen.getByLabelText(/project filter/i), {
        target: { value: "p2" },
      });
    });
    await flush();

    expect(h.spy).toHaveBeenCalledTimes(2);
    const lastArgs = h.spy.mock.calls[h.spy.mock.calls.length - 1][0] as EventQuery;
    expect(lastArgs.project).toBe("p2");
    // The previous-filter row is gone (buffer reset on filter change) and the new
    // row renders the new kind badge.
    const rows = screen.getAllByTestId("evt-row");
    expect(rows).toHaveLength(1);
    expect(within(rows[0]).getByText("log")).toBeInTheDocument();
  });

  it("(e) caps the buffer, dropping the oldest", async () => {
    // Distinct kinds so the progress-collapse never folds them — this asserts the buffer CAP,
    // not the display transform (consecutive same-phase progress pulses would render as one row).
    const h = makeFakeHistory([
      ev({ id: "old", ts: "2026-06-18T00:00:01.000Z", kind: "started" }),
      ev({ id: "mid", ts: "2026-06-18T00:00:02.000Z", kind: "log" }),
    ]);
    render(
      <EventStreamView token="tkn" makeHistory={() => h.loader} bufferCap={2} />,
    );
    await flush();
    expect(screen.getAllByTestId("evt-row")).toHaveLength(2);

    // A third live event exceeds the cap of 2 → oldest ("old") dropped.
    pushLive(ev({ id: "new", ts: "2026-06-18T00:00:03.000Z", kind: "pr" }));
    await flush();

    const rows = screen.getAllByTestId("evt-row");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("pr")).toBeInTheDocument(); // newest
    expect(screen.queryByText(/"old"/)).not.toBeInTheDocument();
  });

  it("(e2) HIDES heartbeats by default, then folds them into one ×N row when shown", async () => {
    const h = makeFakeHistory([
      ev({ id: "m1", ts: "2026-06-18T00:00:00.000Z", kind: "decision" }),
      ev({ id: "p1", ts: "2026-06-18T00:00:01.000Z", kind: "progress" }),
      ev({ id: "p2", ts: "2026-06-18T00:00:02.000Z", kind: "progress" }),
      ev({ id: "p3", ts: "2026-06-18T00:00:03.000Z", kind: "progress" }),
    ]);
    render(<EventStreamView token="tkn" makeHistory={() => h.loader} />);
    await flush();
    // DEFAULT: heartbeats hidden — only the milestone shows, no progress flood, no ×N.
    expect(screen.getAllByTestId("evt-row")).toHaveLength(1);
    expect(within(screen.getByTestId("evt-row")).getByText("decision")).toBeInTheDocument();
    expect(screen.queryByText("×3")).not.toBeInTheDocument();

    // Toggle "Show heartbeats (3)" → the three pulses fold into ONE collapsed ×3 row (still no flood).
    await userEvent.click(screen.getByRole("button", { name: /show heartbeats/i }));
    expect(screen.getAllByTestId("evt-row")).toHaveLength(2); // milestone + one collapsed progress row
    expect(screen.getByText("×3")).toBeInTheDocument();
  });

  it("(f) the pause toggle freezes display updates", async () => {
    const h = makeFakeHistory([ev({ id: "h1" })]);
    render(<EventStreamView token="tkn" makeHistory={() => h.loader} />);
    await flush();
    expect(screen.getAllByTestId("evt-row")).toHaveLength(1);

    await userEvent.click(screen.getByRole("button", { name: /^pause$/i }));
    pushLive(ev({ id: "while-paused", ts: "2026-06-18T00:00:09.000Z", kind: "pr" }));
    await flush();
    // Still 1 row — the live event did not enter the display while paused.
    expect(screen.getAllByTestId("evt-row")).toHaveLength(1);

    // Resume, then a new frame folds the current tail in.
    await userEvent.click(screen.getByRole("button", { name: /^resume$/i }));
    pushLive(ev({ id: "after-resume", ts: "2026-06-18T00:00:10.000Z", kind: "log" }));
    await flush();
    expect(screen.getAllByTestId("evt-row").length).toBeGreaterThan(1);
  });

  it("(g) tears the socket down on filter change and on unmount (no leak)", async () => {
    const h = makeFakeHistory([ev({ id: "h1" })]);
    const { unmount } = render(
      <EventStreamView
        token="tkn"
        projects={["p1", "p2"]}
        makeHistory={() => h.loader}
      />,
    );
    await flush();
    const first = FakeWebSocket.last();
    expect(first.closed).toBe(false);

    // Filter change re-subscribes: the old socket must be closed and a new one opened.
    await act(async () => {
      fireEvent.change(screen.getByLabelText(/project filter/i), {
        target: { value: "p2" },
      });
    });
    await flush();
    expect(first.closed).toBe(true);
    expect(FakeWebSocket.instances.length).toBeGreaterThan(1);

    const second = FakeWebSocket.last();
    unmount();
    expect(second.closed).toBe(true);
  });
});
