// Typed REST client over the conductor-api gateway (ADR-0025/0026). A thin
// wrapper around fetch: it attaches "Authorization: Bearer <token>" to every
// request, maps non-2xx responses to a typed ApiError carrying the status and the
// gateway's {error} body, and exposes one method per gateway endpoint with the
// exact request/response shapes from cmd/conductor-api.
//
// The token is held in memory only and is NEVER logged or serialized into error
// messages. The WebSocket surface lives in useEventStream.ts (browsers cannot set
// an Authorization header, so /ws takes ?token=).
import type {
  Event,
  EventQuery,
  Host,
  IntakeResult,
  Project,
  StatusSummary,
  Task,
} from "./types.ts";

// ApiError is the single typed failure for every non-2xx gateway response. It
// carries the HTTP status and the gateway's secret-free {error} message (or a
// fallback). It deliberately does NOT carry the request or any auth material.
export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

// ClientConfig configures the REST base URL and bearer token. baseUrl defaults to
// VITE_API_BASE (same-origin "" when unset); token is required for authed calls.
export interface ClientConfig {
  baseUrl?: string;
  token: string;
}

// defaultBaseUrl reads the build-time API base, falling back to same-origin "".
export function defaultBaseUrl(): string {
  return import.meta.env.VITE_API_BASE ?? "";
}

export class ApiClient {
  private readonly baseUrl: string;
  private readonly token: string;

  constructor(config: ClientConfig) {
    // Strip a trailing slash so path joins are unambiguous.
    this.baseUrl = (config.baseUrl ?? defaultBaseUrl()).replace(/\/$/, "");
    this.token = config.token;
  }

  // --- read endpoints (server.go) ---

  listProjects(): Promise<Project[]> {
    return this.request<Project[]>("GET", "/projects");
  }

  listTasks(projectId: string): Promise<Task[]> {
    return this.request<Task[]>(
      "GET",
      `/projects/${encodeURIComponent(projectId)}/tasks`,
    );
  }

  listHosts(): Promise<Host[]> {
    return this.request<Host[]>("GET", "/hosts");
  }

  status(): Promise<StatusSummary> {
    return this.request<StatusSummary>("GET", "/status");
  }

  listEvents(params: EventQuery = {}): Promise<Event[]> {
    return this.request<Event[]>("GET", `/events${eventQueryString(params)}`);
  }

  // --- control endpoints (control.go) ---

  onboard(repo: string, baseBranch?: string): Promise<Project> {
    const body: { repo: string; base_branch?: string } = { repo };
    if (baseBranch !== undefined) {
      body.base_branch = baseBranch;
    }
    return this.request<Project>("POST", "/projects", body);
  }

  // intake POSTs RAW scenario YAML (text/plain), not JSON — mirrors handleIntake
  // which reads the body verbatim and parses it as YAML.
  intake(projectId: string, yaml: string): Promise<IntakeResult> {
    return this.request<IntakeResult>(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/intake`,
      yaml,
      "text/plain; charset=utf-8",
    );
  }

  pause(projectId: string): Promise<{ project: string; paused: boolean }> {
    return this.request("POST", `/projects/${encodeURIComponent(projectId)}/pause`);
  }

  resume(projectId: string): Promise<{ project: string; paused: boolean }> {
    return this.request("POST", `/projects/${encodeURIComponent(projectId)}/resume`);
  }

  abort(
    projectId: string,
  ): Promise<{ project: string; aborted_task: string }> {
    return this.request("POST", `/projects/${encodeURIComponent(projectId)}/abort`);
  }

  approve(
    projectId: string,
    taskId?: string,
  ): Promise<{ project: string; approved_task: string }> {
    const body = taskId !== undefined ? { task_id: taskId } : undefined;
    return this.request(
      "POST",
      `/projects/${encodeURIComponent(projectId)}/approve`,
      body,
    );
  }

  // --- core request plumbing ---

  // request issues a single authed call. A plain object body is JSON-encoded; a
  // string body is sent verbatim with the given contentType (used for raw YAML
  // intake). Non-2xx → ApiError(status, gateway {error} message).
  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    contentType?: string,
  ): Promise<T> {
    const headers: Record<string, string> = {
      Authorization: `Bearer ${this.token}`,
    };

    let payload: string | undefined;
    if (typeof body === "string") {
      payload = body;
      headers["Content-Type"] = contentType ?? "text/plain; charset=utf-8";
    } else if (body !== undefined) {
      payload = JSON.stringify(body);
      headers["Content-Type"] = "application/json";
    }

    const init: RequestInit = { method, headers };
    if (payload !== undefined) {
      init.body = payload;
    }

    const res = await fetch(`${this.baseUrl}${path}`, init);

    if (!res.ok) {
      throw new ApiError(res.status, await errorMessage(res));
    }

    // 204 / empty body → undefined; all gateway 2xx responses carry JSON.
    const text = await res.text();
    if (text.length === 0) {
      return undefined as T;
    }
    return JSON.parse(text) as T;
  }
}

// errorMessage extracts the gateway's {error: string} message from a failed
// response, falling back to a generic status line. Never throws (a non-JSON body
// is tolerated) and never includes the token.
async function errorMessage(res: Response): Promise<string> {
  try {
    const data: unknown = await res.json();
    if (
      data !== null &&
      typeof data === "object" &&
      "error" in data &&
      typeof (data as { error: unknown }).error === "string"
    ) {
      return (data as { error: string }).error;
    }
  } catch {
    // non-JSON / empty body — fall through to the generic message.
  }
  return `request failed with status ${res.status}`;
}

// eventQueryString builds the GET /events query string from an EventQuery,
// omitting empty values and only emitting intervention=1 when true (matching the
// gateway's isTruthy parsing).
export function eventQueryString(params: EventQuery): string {
  const q = new URLSearchParams();
  if (params.project) q.set("project", params.project);
  if (params.task) q.set("task", params.task);
  if (params.phase) q.set("phase", params.phase);
  if (params.kind) q.set("kind", params.kind);
  if (params.intervention) q.set("intervention", "1");
  if (params.since) q.set("since", params.since);
  if (params.limit !== undefined) q.set("limit", String(params.limit));
  const s = q.toString();
  return s.length > 0 ? `?${s}` : "";
}
