// useFleet tests: drive the hook with a fake FleetClient (no network) and a stubbed
// WebSocket, asserting it aggregates the fleet, that refresh() re-fetches, and that
// an injected relevant event triggers an out-of-band refresh. The poll interval is
// driven with fake timers.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render } from "@testing-library/react";
import { useFleet } from "./useFleet.ts";
import type { FleetClient, FleetSnapshot } from "./useFleet.ts";
import { ApiError } from "../api/client.ts";
import type { Event, Host, Project, StatusSummary, Task } from "../api/types.ts";

// FakeWebSocket lets us drive onmessage to simulate a live event arriving.
class FakeWebSocket {
  static last: FakeWebSocket | null = null;
  url: string;
  onopen: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(url: string) {
    this.url = url;
    FakeWebSocket.last = this;
  }
  close(): void {}
}

const PROJECTS: Project[] = [
  {
    id: "p1",
    repo: "git@example.com:p1.git",
    base_branch: "main",
    host_id: "h1",
    readiness: "ready",
    recipe_pointer: "r1",
    governance_policy: "auto",
    paused: false,
  },
];

const HOSTS: Host[] = [
  {
    id: "h1",
    capabilities: ["go", "node"],
    last_heartbeat: "2026-06-18T00:00:00Z",
    heartbeat_age_seconds: 5,
  },
];

const TASKS: Task[] = [
  {
    id: "t1",
    project_id: "p1",
    lane: "build",
    tier: "core",
    status: "running",
    requires: [],
    deps: [],
    branch: "feat/t1",
    scenario_id: "s1",
    retry_count: 0,
    abort_requested: false,
    approved: false,
  },
];

const STATUS: StatusSummary = {
  projects: 1,
  hosts: 1,
  leases: [
    { project_id: "p1", host_id: "h1", task_id: "t1", acquired_at: "2026-06-18T00:00:00Z" },
  ],
  generated_at: "2026-06-18T00:00:00Z",
};

// makeFakeClient returns a client whose calls are spies so the test can count them.
function makeFakeClient(): FleetClient & {
  statusSpy: ReturnType<typeof vi.fn>;
} {
  const statusSpy = vi.fn(async () => STATUS);
  return {
    statusSpy,
    status: statusSpy,
    listProjects: vi.fn(async () => PROJECTS),
    listHosts: vi.fn(async () => HOSTS),
    listTasks: vi.fn(async (): Promise<Task[]> => TASKS),
  };
}

// Harness renders useFleet and publishes its latest snapshot to the test.
function Harness({
  client,
  onSnapshot,
}: {
  client: FleetClient;
  onSnapshot: (s: FleetSnapshot) => void;
}) {
  const fleet = useFleet({
    token: "tkn",
    makeClient: () => client,
    refreshIntervalMs: 5000,
  });
  onSnapshot(fleet);
  return null;
}

let snapshot: FleetSnapshot | null = null;

beforeEach(() => {
  vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
  FakeWebSocket.last = null;
  snapshot = null;
});

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

// flush lets the pending promise microtasks (the parallel fetches) settle.
async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("useFleet", () => {
  // Regression (caught in live verification): the DEFAULT makeClient must be a STABLE
  // module-scope reference. An inline default built a fresh ApiClient every render, so
  // `client`/`refresh` changed identity every render, the poll effect re-ran every
  // render, and it fired an unbounded request storm (the browser hit
  // ERR_INSUFFICIENT_RESOURCES and the UI flickered). With NO makeClient passed (the
  // production web path) refresh must be referentially stable across re-renders. fetch
  // is stubbed to never resolve so the only thing under test is render-driven
  // (in)stability, not network behavior. (The other tests pass a fake that returns the
  // SAME object, which masked this — the bug only bites when makeClient mints a fresh
  // client per call, i.e. the default.)
  it("uses a stable default client → refresh is referentially stable across renders (no storm)", async () => {
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>(() => {})));
    const seen: Array<FleetSnapshot["refresh"]> = [];
    function Probe() {
      const fleet = useFleet({ token: "tkn" }); // DEFAULT makeClient (production web path)
      seen.push(fleet.refresh);
      return null;
    }
    let view!: ReturnType<typeof render>;
    await act(async () => {
      view = render(<Probe />);
    });
    await act(async () => {
      view.rerender(<Probe />);
    });
    await act(async () => {
      view.rerender(<Probe />);
    });
    expect(seen.length).toBeGreaterThanOrEqual(3);
    // A stable default keeps one refresh identity; the pre-fix inline default produced a
    // new one on every render (the storm's root cause).
    expect(new Set(seen).size).toBe(1);
  });

  it("aggregates status, projects, hosts, and per-project tasks", async () => {
    const client = makeFakeClient();
    render(<Harness client={client} onSnapshot={(s) => (snapshot = s)} />);
    await flush();

    expect(snapshot).not.toBeNull();
    const s = snapshot!;
    expect(s.status).toEqual(STATUS);
    expect(s.projects).toEqual(PROJECTS);
    expect(s.hosts).toEqual(HOSTS);
    expect(s.tasksByProject["p1"]).toEqual(TASKS);
    expect(s.leasesByProject["p1"]).toHaveLength(1);
    expect(s.error).toBeNull();
    expect(s.loading).toBe(false);
  });

  it("refresh() re-fetches the fleet", async () => {
    const client = makeFakeClient();
    render(<Harness client={client} onSnapshot={(s) => (snapshot = s)} />);
    await flush();

    const before = client.statusSpy.mock.calls.length;
    await act(async () => {
      await snapshot!.refresh();
    });
    expect(client.statusSpy.mock.calls.length).toBe(before + 1);
  });

  it("re-fetches on the periodic interval", async () => {
    vi.useFakeTimers();
    const client = makeFakeClient();
    render(<Harness client={client} onSnapshot={(s) => (snapshot = s)} />);
    // Initial load.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    const initial = client.statusSpy.mock.calls.length;
    // Advance past one interval tick.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5000);
    });
    expect(client.statusSpy.mock.calls.length).toBeGreaterThan(initial);
  });

  it("triggers a refresh when a relevant (merge) event arrives", async () => {
    const client = makeFakeClient();
    render(<Harness client={client} onSnapshot={(s) => (snapshot = s)} />);
    await flush();

    const before = client.statusSpy.mock.calls.length;
    const mergeEvent: Event = {
      id: "e1",
      ts: "2026-06-18T00:01:00Z",
      project: "p1",
      task: "t1",
      phase: "merge",
      kind: "merge",
      payload: {},
    };
    await act(async () => {
      FakeWebSocket.last!.onopen?.();
      FakeWebSocket.last!.onmessage?.({ data: JSON.stringify(mergeEvent) } as MessageEvent);
    });
    await flush();

    expect(snapshot!.lastEventAt).not.toBeNull();
    expect(client.statusSpy.mock.calls.length).toBeGreaterThan(before);
  });

  it("surfaces a 401 as an unauthorized FleetError", async () => {
    const client = makeFakeClient();
    client.status = vi.fn(async () => {
      throw new ApiError(401, "unauthorized");
    });
    render(<Harness client={client} onSnapshot={(s) => (snapshot = s)} />);
    await flush();

    expect(snapshot!.error).not.toBeNull();
    expect(snapshot!.error?.unauthorized).toBe(true);
    expect(snapshot!.error?.status).toBe(401);
  });
});
