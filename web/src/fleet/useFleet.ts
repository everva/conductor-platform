// useFleet: the fleet dashboard's data layer (3B-1). It aggregates the gateway's
// read surface (ADR-0025) into one typed snapshot and keeps it live via two
// mechanisms:
//   1. a periodic REST refresh (status / projects / hosts / per-project tasks),
//   2. a WebSocket subscription (useEventStream) that nudges a refresh whenever a
//      relevant event arrives (esp. merge / decision / intervention-needed) so the
//      UI reacts to fleet changes faster than the poll interval.
//
// The ApiClient is INJECTABLE (makeClient) so tests can drive a fake without a
// network and without duplicating any API logic. All ApiErrors surface on `error`
// (a 401 lets the app prompt re-auth); a refresh never throws to the caller.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ApiClient, ApiError } from "../api/client.ts";
import type { Host, Lease, Project, StatusSummary, Task } from "../api/types.ts";
import { useEventStream } from "../api/useEventStream.ts";
import type { Event, EventQuery, Kind } from "../api/types.ts";
import type { StreamState } from "../api/useEventStream.ts";

// FleetClient is the read surface useFleet consumes. ApiClient satisfies it; tests
// pass a lightweight fake implementing exactly these methods.
export interface FleetClient {
  status(): Promise<StatusSummary>;
  listProjects(): Promise<Project[]>;
  listHosts(): Promise<Host[]>;
  listTasks(projectId: string): Promise<Task[]>;
}

export interface FleetSnapshot {
  status: StatusSummary | null;
  projects: Project[];
  hosts: Host[];
  // tasksByProject is keyed by project id; absent until that project's tasks load.
  tasksByProject: Record<string, Task[]>;
  // leasesByProject groups the /status leases by project for quick lookup.
  leasesByProject: Record<string, Lease[]>;
  loading: boolean;
  error: FleetError | null;
  // lastEventAt is the wall-clock time (ms) of the most recent WS event, or null.
  lastEventAt: number | null;
  // streamState is the live WebSocket connection state.
  streamState: StreamState;
  // recentEvents is the WS buffer (oldest→newest), capped by the stream.
  recentEvents: Event[];
  // refresh re-runs the REST fetch immediately (returns when done).
  refresh: () => Promise<void>;
}

// FleetError is the surfaced load failure. `unauthorized` is true on a 401 so the
// app can prompt re-auth; `status` is the HTTP status when it was an ApiError.
export interface FleetError {
  message: string;
  status: number | null;
  unauthorized: boolean;
}

export interface UseFleetOptions {
  token: string;
  // makeClient builds the read client (default: real ApiClient). Injectable for tests.
  makeClient?: (token: string) => FleetClient;
  // refreshIntervalMs is the periodic REST poll cadence. Defaults to 5000ms.
  refreshIntervalMs?: number;
  // selectedProjectId optionally narrows the WS subscription to one project.
  selectedProjectId?: string | undefined;
  // enabled gates all activity (no token before auth). Defaults to true.
  enabled?: boolean;
}

// Kinds that should trigger an out-of-band refresh the moment they arrive.
const REFRESH_KINDS: ReadonlySet<Kind> = new Set<Kind>([
  "merge",
  "decision",
  "intervention-needed",
  "pr",
]);

function toFleetError(err: unknown): FleetError {
  if (err instanceof ApiError) {
    return {
      message:
        err.status === 401 ? "Authentication failed." : `Gateway error (${err.status}).`,
      status: err.status,
      unauthorized: err.status === 401,
    };
  }
  return { message: "Could not reach the gateway.", status: null, unauthorized: false };
}

export function useFleet(opts: UseFleetOptions): FleetSnapshot {
  const {
    token,
    makeClient = (t) => new ApiClient({ token: t }),
    refreshIntervalMs = 5000,
    selectedProjectId,
    enabled = true,
  } = opts;

  const [status, setStatus] = useState<StatusSummary | null>(null);
  const [projects, setProjects] = useState<Project[]>([]);
  const [hosts, setHosts] = useState<Host[]>([]);
  const [tasksByProject, setTasksByProject] = useState<Record<string, Task[]>>({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<FleetError | null>(null);
  const [lastEventAt, setLastEventAt] = useState<number | null>(null);

  // The client is rebuilt only when the token (or factory) changes.
  const client = useMemo(() => makeClient(token), [makeClient, token]);

  // Guard against setting state after unmount (a slow fetch resolving late).
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const refresh = useCallback(async () => {
    if (!enabled || token.length === 0) {
      return;
    }
    if (mountedRef.current) {
      setLoading(true);
    }
    try {
      const [statusRes, projectsRes, hostsRes] = await Promise.all([
        client.status(),
        client.listProjects(),
        client.listHosts(),
      ]);
      // Fetch tasks for every known project in parallel; tolerate per-project gaps.
      const taskEntries = await Promise.all(
        projectsRes.map(async (p): Promise<[string, Task[]]> => {
          const tasks = await client.listTasks(p.id);
          return [p.id, tasks];
        }),
      );
      if (!mountedRef.current) {
        return;
      }
      setStatus(statusRes);
      setProjects(projectsRes);
      setHosts(hostsRes);
      setTasksByProject(Object.fromEntries(taskEntries));
      setError(null);
    } catch (err) {
      if (mountedRef.current) {
        setError(toFleetError(err));
      }
    } finally {
      if (mountedRef.current) {
        setLoading(false);
      }
    }
  }, [client, enabled, token]);

  // Initial load + periodic poll. The interval is cleared on unmount / dep change.
  useEffect(() => {
    if (!enabled || token.length === 0) {
      return;
    }
    void refresh();
    const id = setInterval(() => {
      void refresh();
    }, refreshIntervalMs);
    return () => {
      clearInterval(id);
    };
  }, [refresh, refreshIntervalMs, enabled, token]);

  // Live event subscription. A project filter narrows the stream when one is selected.
  const filter = useMemo<EventQuery | undefined>(
    () => (selectedProjectId ? { project: selectedProjectId } : undefined),
    [selectedProjectId],
  );
  const stream = useEventStream({
    token,
    enabled: enabled && token.length > 0,
    ...(filter ? { filter } : {}),
  });

  // When a relevant event lands, stamp lastEventAt and nudge a refresh so the
  // tables reflect the change without waiting for the next poll tick.
  const latest = stream.latest;
  useEffect(() => {
    if (latest === null) {
      return;
    }
    setLastEventAt(Date.now());
    if (REFRESH_KINDS.has(latest.kind)) {
      void refresh();
    }
  }, [latest, refresh]);

  const leasesByProject = useMemo<Record<string, Lease[]>>(() => {
    const grouped: Record<string, Lease[]> = {};
    for (const lease of status?.leases ?? []) {
      (grouped[lease.project_id] ??= []).push(lease);
    }
    return grouped;
  }, [status]);

  return {
    status,
    projects,
    hosts,
    tasksByProject,
    leasesByProject,
    loading,
    error,
    lastEventAt,
    streamState: stream.state,
    recentEvents: stream.events,
    refresh,
  };
}
