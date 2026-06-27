// Shared-cockpit proof (4A-2, ADR-0028): mount <FleetDashboard> in FORK MODE —
// token="" + injected transport-backed REST clients + an injected EventTransport —
// and prove it renders ENTIRELY over those seams with NO token and NO real network
// or WebSocket. This is the honest "consumable by both" test: if the dashboard
// couldn't mount token-free over injected transports, it would fail here. The web
// (token) path stays covered by the existing component/hook tests.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import {
  ApiClient,
  FleetDashboard,
  type Event,
  type EventQuery,
  type EventSubscription,
  type EventTransport,
  type FleetDashboardProps,
  type Host,
  type IntakeResult,
  type Project,
  type StatusSummary,
  type Task,
} from "./cockpit.ts";

afterEach(() => {
  vi.restoreAllMocks();
});

// --- canned fleet the injected REST clients return (no network) ---
const PROJECT: Project = {
  id: "fork-proj",
  repo: "git@example.com:fork-proj.git",
  base_branch: "main",
  host_id: "fork-host",
  readiness: "ready",
  recipe_pointer: "r1",
  governance_policy: "auto",
  paused: false,
};

const HOST: Host = {
  id: "fork-host",
  capabilities: ["go", "node"],
  last_heartbeat: "2026-06-18T00:00:00Z",
  heartbeat_age_seconds: 5,
};

const TASK: Task = {
  id: "fork-task",
  project_id: "fork-proj",
  lane: "build",
  tier: "core",
  status: "running",
  requires: [],
  deps: [],
  branch: "feat/fork-task",
  scenario_id: "s1",
  retry_count: 0,
  abort_requested: false,
  approved: false,
};

const STATUS: StatusSummary = {
  projects: 1,
  hosts: 1,
  leases: [],
  generated_at: "2026-06-18T00:00:00Z",
};

const LIVE_EVENT: Event = {
  id: "ev-1",
  ts: "2026-06-18T00:01:00Z",
  project: "fork-proj",
  task: "fork-task",
  phase: "develop",
  // A MILESTONE kind: the recent-events ticker drops heartbeat "progress" pulses, so this proves
  // the live tail reaches the UI with an event the ticker actually surfaces.
  kind: "started",
  payload: { pct: 42 },
};

// Captured callbacks from the most recent subscribe(), so the test can drive them
// (mirrors eventTransport.test.tsx's FakeEventTransport — no real WebSocket).
interface Captured {
  filter?: EventQuery | undefined;
  onOpen: () => void;
  onEvent: (e: Event) => void;
  onClose: () => void;
}

class FakeEventTransport implements EventTransport {
  captured: Captured | null = null;
  subscribeCalls = 0;
  closed = false;

  subscribe(opts: {
    filter?: EventQuery;
    onOpen: () => void;
    onEvent: (e: Event) => void;
    onClose: () => void;
  }): EventSubscription {
    this.subscribeCalls += 1;
    this.captured = opts;
    return {
      close: () => {
        this.closed = true;
      },
    };
  }
}

// FORK-style REST factories: zero/one-arg factories that IGNORE the token (exactly
// how the fork passes `() => new ApiClient({ transport })`). Here they return the
// canned fleet directly — no transport even needed — proving the components consume
// whatever the host injects, token-free.
const makeReadClient: FleetDashboardProps["makeClient"] = () => ({
  status: async (): Promise<StatusSummary> => STATUS,
  listProjects: async (): Promise<Project[]> => [PROJECT],
  listHosts: async (): Promise<Host[]> => [HOST],
  listTasks: async (): Promise<Task[]> => [TASK],
});

const makeControlClient: FleetDashboardProps["makeControlClient"] = () => ({
  pause: async () => ({ project: PROJECT.id, paused: true }),
  resume: async () => ({ project: PROJECT.id, paused: false }),
  abort: async () => ({ project: PROJECT.id, aborted_task: "" }),
  approve: async () => ({ project: PROJECT.id, approved_task: "" }),
  reject: async () => ({ project: PROJECT.id, rejected_task: "" }),
  retry: async () => ({ project: PROJECT.id, task: "", status: "ready" }),
});

const makeIntakeClient: FleetDashboardProps["makeIntakeClient"] = () => ({
  distill: async () => ({ scenarios: [], yaml: "" }),
  intake: async (): Promise<IntakeResult> => ({ created: [], skipped: [] }),
});

// flush lets the parallel REST fetches' microtasks settle (mirrors useFleet.test).
async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("cockpit barrel: FleetDashboard token-free over injected transports", () => {
  it("renders the fleet over injected REST + EventTransport with NO token and NO real socket", async () => {
    const fakeTransport = new FakeEventTransport();
    let unauthorized = false;

    render(
      <FleetDashboard
        // Empty token — the whole point: the fork holds no token (ADR-0027).
        token=""
        onUnauthorized={() => {
          unauthorized = true;
        }}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        eventTransport={fakeTransport}
      />,
    );
    await flush();

    // Redesign E1: the Command Center board is now the DEFAULT surface. This test
    // exercises the FLEET view's injected-REST + ticker wiring, so switch to it.
    act(() => {
      screen.getByRole("tab", { name: "Fleet" }).click();
    });

    // REST flowed via the injected clients despite the empty token: the fleet panel
    // shows the injected project + host (decoupled readiness, enabled via the fork
    // contract). 401 was never bubbled.
    expect(unauthorized).toBe(false);
    expect(screen.getByText(PROJECT.id)).toBeInTheDocument();
    expect(screen.getByText(PROJECT.repo)).toBeInTheDocument();
    expect(screen.getByText(HOST.id)).toBeInTheDocument();

    // The live stream went through the INJECTED EventTransport — no real WebSocket
    // was constructed (jsdom has none, and we stubbed nothing).
    expect(fakeTransport.subscribeCalls).toBe(1);
    expect(fakeTransport.captured).not.toBeNull();

    // Before onOpen the status bar shows the connecting state.
    expect(
      screen.getByLabelText("Stream connecting"),
    ).toBeInTheDocument();

    // Driving the injected transport's callbacks updates the UI: onOpen flips the
    // status bar to live...
    act(() => {
      fakeTransport.captured!.onOpen();
    });
    expect(screen.getByLabelText("Stream open")).toBeInTheDocument();

    // ...and onEvent folds the event into the recent-events ticker (proving the live
    // tail reaches the UI entirely over the injected transport).
    act(() => {
      fakeTransport.captured!.onEvent(LIVE_EVENT);
    });
    const ticker = screen.getByLabelText("Recent events");
    expect(within(ticker).getByText(`${LIVE_EVENT.phase}/${LIVE_EVENT.kind}`)).toBeInTheDocument();
    // The ticker renders the location as "project·task" in one node; match the
    // project as a substring (proves the injected event reached the live ticker).
    expect(
      within(ticker).getByText((_, el) => el?.textContent === `${LIVE_EVENT.project}·${LIVE_EVENT.task}`),
    ).toBeInTheDocument();
  });

  it("⌘K opens the command palette wired to the live fleet; choosing a surface navigates (E4)", async () => {
    const fakeTransport = new FakeEventTransport();
    render(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        eventTransport={fakeTransport}
      />,
    );
    await flush();

    // The global ⌘K shortcut opens the palette from anywhere (here, the board).
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", metaKey: true }));
    });
    const palette = screen.getByRole("dialog", { name: "Command palette" });

    // It is wired to the LIVE fleet snapshot: the injected task shows as a
    // jump-to-session row (proving FleetDashboard hands it fleet.tasksByProject).
    expect(within(palette).getByText(TASK.id)).toBeInTheDocument();

    // Choosing "Go to Fleet" routes the cockpit to the Fleet surface via the same
    // setTab seam a tab click uses (runPaletteAction). mousedown matches the item
    // handler (it runs before the input blurs).
    act(() => {
      fireEvent.mouseDown(within(palette).getByText("Go to Fleet"));
    });
    expect(screen.getByRole("tab", { name: "Fleet" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    // The palette closed as part of executing the action.
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
  });

  it("selection with a task (host select) opens the requested session's SessionView (Q0)", async () => {
    const fakeTransport = new FakeEventTransport();
    render(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        makeScenarioClient={() => ({ listScenarios: async () => [] })}
        makeHistory={() => ({ listEvents: async () => [] })}
        eventTransport={fakeTransport}
        selection={{ project: PROJECT.id, task: TASK.id }}
      />,
    );

    // A task selection resolves against the live fleet and opens the task's SessionView —
    // the SAME surface a board drill-in shows (the tabbed board is replaced). findByRole
    // rides the async fleet load + SessionView mount.
    expect(await screen.findByRole("region", { name: "Session" })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Board" })).not.toBeInTheDocument();
  });

  it("selection to an unknown task is a no-op (stays on the board, Q0)", async () => {
    const fakeTransport = new FakeEventTransport();
    render(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        eventTransport={fakeTransport}
        selection={{ project: PROJECT.id, task: "no-such-task" }}
      />,
    );
    await flush();

    // No matching task → no SessionView; the board (default surface) is still shown.
    expect(screen.queryByRole("region", { name: "Session" })).not.toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Board" })).toBeInTheDocument();
  });

  it("a project-only selection returns from an open session to the board (Q0)", async () => {
    const fakeTransport = new FakeEventTransport();
    const { rerender } = render(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        makeScenarioClient={() => ({ listScenarios: async () => [] })}
        makeHistory={() => ({ listEvents: async () => [] })}
        eventTransport={fakeTransport}
        selection={{ project: PROJECT.id, task: TASK.id }}
      />,
    );
    // First a task selection opens the session.
    expect(await screen.findByRole("region", { name: "Session" })).toBeInTheDocument();

    // A NEW project-only selection (no task) clears the session and shows the board overview —
    // proving the host-owned selection drives the surface (not just the deep-link).
    rerender(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        makeScenarioClient={() => ({ listScenarios: async () => [] })}
        makeHistory={() => ({ listEvents: async () => [] })}
        eventTransport={fakeTransport}
        selection={{ project: PROJECT.id }}
      />,
    );
    expect(await screen.findByRole("tab", { name: "Board" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Session" })).not.toBeInTheDocument();
  });

  it("a project-only selection scopes the board; Show all clears it (Q1)", async () => {
    const fakeTransport = new FakeEventTransport();
    render(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        eventTransport={fakeTransport}
        selection={{ project: PROJECT.id }}
      />,
    );
    await flush();

    // The board (default surface) is scoped to the selected project: the "Show all" clear control
    // appears only while scoped, proving selection → selectedProjectId → board scope is wired.
    const showAll = await screen.findByRole("button", { name: "Show all" });
    expect(showAll).toBeInTheDocument();

    // Clearing the scope removes the control (back to the full-fleet board).
    act(() => {
      fireEvent.click(showAll);
    });
    expect(screen.queryByRole("button", { name: "Show all" })).not.toBeInTheDocument();
  });

  it("surface lock (B — separate windows): renders ONLY that surface, no tab-bar, no ⌘K palette", async () => {
    const fakeTransport = new FakeEventTransport();
    render(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        makeHistory={() => ({ listEvents: async () => [] })}
        eventTransport={fakeTransport}
        surface="events"
      />,
    );
    await flush();

    // The locked Events surface is mounted directly (not the default board)...
    expect(screen.getByRole("region", { name: /event stream/i })).toBeInTheDocument();
    // ...and the segmented tab-bar is gone — a locked window can't switch surfaces.
    expect(screen.queryByRole("tablist", { name: "Dashboard view" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Board" })).not.toBeInTheDocument();

    // ⌘K does NOT open the palette here (it would navigate surfaces, which the lock forbids).
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", metaKey: true }));
    });
    expect(screen.queryByRole("dialog", { name: "Command palette" })).not.toBeInTheDocument();
  });

  it("surface lock (B): a board-locked window shows the board with no tab-bar", async () => {
    const fakeTransport = new FakeEventTransport();
    render(
      <FleetDashboard
        token=""
        onUnauthorized={() => {}}
        makeClient={makeReadClient}
        makeControlClient={makeControlClient}
        makeIntakeClient={makeIntakeClient}
        eventTransport={fakeTransport}
        surface="board"
      />,
    );
    await flush();

    // The board surface renders (its lifecycle columns), with NO surface tab-bar.
    expect(screen.getByRole("region", { name: /command center/i })).toBeInTheDocument();
    expect(screen.queryByRole("tablist", { name: "Dashboard view" })).not.toBeInTheDocument();
  });

  it("exposes the transport seams the fork wires (ApiClient over an injected HttpTransport)", async () => {
    // The fork's REST factory is literally `() => new ApiClient({ transport })`. Prove
    // that exact shape works token-free through the barrel's re-exported ApiClient +
    // HttpTransport seam: a fake transport returns the canned status JSON, no token.
    const client = new ApiClient({
      transport: {
        send: async () => ({
          status: 200,
          ok: true,
          body: JSON.stringify(STATUS),
        }),
      },
    });
    await expect(client.status()).resolves.toEqual(STATUS);
  });
});
