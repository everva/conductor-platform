// cockpit.ts — the PUBLIC, transport-agnostic cockpit surface (ADR-0028, 4A-2).
//
// This barrel is the single explicit mount surface for the Conductor cockpit,
// consumed by BOTH:
//   * the web App (this repo) — the FIRST consumer, in TOKEN MODE: it passes a real
//     token and lets the components build the default FetchTransport/WebSocketTransport;
//   * (4B) the fork webview — the SECOND consumer, in FORK/TRANSPORT MODE: the
//     extension host owns auth (SecretStorage) and injects an `eventTransport`
//     (postMessage bridge) plus transport-backed `make*Client` factories
//     (e.g. `() => new ApiClient({ transport })`), passing `token=""`. The token
//     NEVER enters the webview (ADR-0027).
//
// Because both modes mount through this one barrel, "what is shared" is explicit and
// the boundary is enforceable. It re-exports ONLY the mount component + the transport
// seams + domain types. It MUST NOT pull in web-only bootstrap (auth/session/main):
// readiness is the host's job (decoupled from the token), auth is the transport's
// job. The ESLint `no-restricted-imports` lock on src/{fleet,events,intake,api}
// keeps the shared modules free of that bootstrap so this surface can't rot.
//
// `events.gen.ts` stays the SINGLE SOURCE for the event vocabulary — it is consumed
// transitively through the api seam (types.ts re-exports Event/Phase/Kind); it is
// never copied here.

// --- Mount component (the one cockpit a host renders) ---
export { FleetDashboard } from "./fleet/FleetDashboard.tsx";
export type { FleetDashboardProps, DashboardTab } from "./fleet/FleetDashboard.tsx";

// --- Intake surface (Faz-Q / Q3): mountable STANDALONE so a host can give Intake its own
// window (the fork's editor-area "New Work" panel) instead of burying it in a cockpit tab. It
// consumes only the narrow IntakeClient (distill/intake) — the host injects a bridge-backed one. ---
export { IntakeChat } from "./intake/IntakeChat.tsx";
export type { IntakeChatProps, IntakeClient } from "./intake/IntakeChat.tsx";

// --- REST seam (auth-agnostic HTTP transport + typed client) ---
export {
  ApiClient,
  ApiError,
  DistillNoScenariosError,
  FetchTransport,
  defaultBaseUrl,
} from "./api/client.ts";
export type {
  HttpTransport,
  HttpRequest,
  HttpResponse,
} from "./api/client.ts";

// --- Event seam (auth-agnostic event transport + live hook) ---
export { WebSocketTransport, useEventStream, wsUrl } from "./api/useEventStream.ts";
export type { EventTransport, EventSubscription } from "./api/useEventStream.ts";

// --- Domain types (the gateway DTO surface; events.gen.ts via the api seam) ---
export type {
  DistillResult,
  Event,
  EventQuery,
  Host,
  IntakeResult,
  Kind,
  Lease,
  Phase,
  Project,
  Scenario,
  StatusSummary,
  Task,
} from "./api/types.ts";
