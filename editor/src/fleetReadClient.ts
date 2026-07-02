// Conductor Platform — host-side authed READ client for the native sessions tree (N2).
//
// vscode-FREE (mirrors controlClient.ts / gateway.ts) so vitest drives it with a stubbed
// `fetch`, headlessly — no electron, no display. It performs the AUTHED reads the native
// "Conductors" TreeView needs:
//   GET /projects             → the projects (Conductors)
//   GET /projects/{id}/tasks  → that project's tasks (sessions)
// Both are wrapped by the gateway's requireAuth (server.go). This is the READ sibling of
// ControlClient (which owns the control POSTs); kept separate so read vs control each stay
// small and testable. It REUSES ControlClient's TokenProvider seam.
//
// TOKEN DISCIPLINE (HARD): the bearer token rides ONLY in the outgoing `Authorization`
// header (the single `authHeader` egress). It NEVER appears in a returned value, a thrown
// error, or a log line — this module logs nothing, and the returned shapes are tolerant
// projections (only the fields the tree renders), never the token. A sentinel-token
// leak-guard test in fleetReadClient.test.ts proves it. Any non-happy outcome (no token,
// non-2xx, network throw) maps to an EMPTY list — the tree then shows its welcome view; the
// client never throws (the tree doesn't branch on a reason, unlike runControl).
import type { TokenProvider } from "./controlClient";

/** A project (Conductor) the tree renders — a tolerant projection of the gateway projectDTO
 * (server.go toProjectDTO). Only the fields the tree shows; the full DTO is tolerated. */
export interface FleetProject {
  readonly id: string;
  readonly readiness: string;
  readonly paused: boolean;
}

/** One rolling window of a claude subscription's utilization (from GET /usage; the davinci probe
 * mirrors api.anthropic.com/api/oauth/usage). `used_pct` is 0-100; `resets_at` an ISO timestamp. */
export interface UsageWindow {
  readonly used_pct: number | null;
  readonly resets_at: string | null;
}

/** A per-subscription usage snapshot (server.go GET /usage value). Tolerant projection: a
 * subscription whose token lacks the profile scope reports `available:false` + a `reason`. */
export interface UsageSnapshot {
  readonly label?: string;
  readonly available?: boolean;
  readonly five_hour?: UsageWindow;
  readonly seven_day?: UsageWindow;
  readonly reason?: string;
  readonly checked_at?: string;
}

/** A task (session) the tree renders — a tolerant projection of the gateway taskDTO
 * (server.go handleProjectTasks). Only the fields the tree shows. */
export interface FleetTask {
  readonly id: string;
  readonly projectId: string;
  readonly lane: string;
  readonly tier: string;
  readonly status: string;
}

/**
 * Host-side authed read client. Construct one per activation with the configured (normalized)
 * gateway base URL + a TokenProvider reading SecretStorage; tests pass a fake provider + a
 * stubbed `fetch`. The token flows solely into the Authorization header of each GET — never
 * to a returned value, a log, or a webview.
 */
export class FleetReadClient {
  readonly #baseUrl: string;
  readonly #tokens: TokenProvider;
  readonly #fetch: typeof fetch;

  constructor(baseUrl: string, tokenProvider: TokenProvider, fetchImpl: typeof fetch = fetch) {
    this.#baseUrl = baseUrl;
    this.#tokens = tokenProvider;
    this.#fetch = fetchImpl;
  }

  /** Lists projects (Conductors): `GET {base}/projects` (authed). Empty on any non-happy path. */
  listProjects(): Promise<FleetProject[]> {
    return this.#getList("/projects", toFleetProjects);
  }

  /** Lists a project's tasks (sessions): `GET {base}/projects/{id}/tasks` (authed). Empty on
   * any non-happy path. */
  listTasks(projectId: string): Promise<FleetTask[]> {
    return this.#getList(`/projects/${encodeURIComponent(projectId)}/tasks`, toFleetTasks);
  }

  /** Fetches the live per-subscription claude usage: `GET {base}/usage` (authed). Returns `{}` on
   * any non-happy path (no token, non-2xx, network/parse error) — the usage bar just hides. The
   * token rides only in the Authorization header. */
  async getUsage(): Promise<Record<string, UsageSnapshot>> {
    const token = await this.#tokens.getToken();
    if (token === undefined || token === "") {
      return {};
    }
    try {
      const res = await this.#fetch(`${this.#baseUrl}/usage`, {
        method: "GET",
        headers: { Authorization: `Bearer ${token}` },
      });
      if (!res.ok) {
        return {};
      }
      const body = (await res.json()) as unknown;
      return body !== null && typeof body === "object" && !Array.isArray(body)
        ? (body as Record<string, UsageSnapshot>)
        : {};
    } catch {
      return {};
    }
  }

  /**
   * Authed GET that maps the JSON body via `project`-tolerant `map`. Returns `[]` (never
   * throws) on: no stored token, non-2xx, or a network/parse error — the tree treats any of
   * these as "nothing to show". The token rides only in the Authorization header.
   */
  async #getList<T>(path: string, map: (body: unknown) => T[]): Promise<T[]> {
    const token = await this.#tokens.getToken();
    if (token === undefined || token === "") {
      return [];
    }
    let res: Response;
    try {
      res = await this.#fetch(`${this.#baseUrl}${path}`, {
        method: "GET",
        headers: { Authorization: `Bearer ${token}` },
      });
    } catch {
      return [];
    }
    if (!res.ok) {
      return [];
    }
    let body: unknown;
    try {
      body = (await res.json()) as unknown;
    } catch {
      return [];
    }
    return map(body);
  }
}

/** Maps an unknown `/projects` payload onto `FleetProject[]`, tolerating the full project DTO:
 * keeps array entries that are objects with a string `id`; coerces `readiness`/`paused`
 * defensively. Anything malformed is dropped — never throws. */
function toFleetProjects(body: unknown): FleetProject[] {
  if (!Array.isArray(body)) {
    return [];
  }
  const out: FleetProject[] = [];
  for (const entry of body) {
    if (entry === null || typeof entry !== "object") {
      continue;
    }
    const rec = entry as Record<string, unknown>;
    if (typeof rec["id"] !== "string") {
      continue;
    }
    out.push({
      id: rec["id"],
      readiness: typeof rec["readiness"] === "string" ? rec["readiness"] : "",
      paused: rec["paused"] === true,
    });
  }
  return out;
}

/** Maps an unknown `/projects/{id}/tasks` payload onto `FleetTask[]`, tolerating the full task
 * DTO: keeps array entries with a string `id`; reads the gateway's snake_case fields. */
function toFleetTasks(body: unknown): FleetTask[] {
  if (!Array.isArray(body)) {
    return [];
  }
  const out: FleetTask[] = [];
  for (const entry of body) {
    if (entry === null || typeof entry !== "object") {
      continue;
    }
    const rec = entry as Record<string, unknown>;
    if (typeof rec["id"] !== "string") {
      continue;
    }
    out.push({
      id: rec["id"],
      projectId: typeof rec["project_id"] === "string" ? rec["project_id"] : "",
      lane: typeof rec["lane"] === "string" ? rec["lane"] : "",
      tier: typeof rec["tier"] === "string" ? rec["tier"] : "",
      status: typeof rec["status"] === "string" ? rec["status"] : "",
    });
  }
  return out;
}
