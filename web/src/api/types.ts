// Typed contracts for the conductor-api gateway (ADR-0025). Field names mirror
// the gateway's JSON tags EXACTLY (snake_case as Go emits) so the client is a
// faithful, drift-free view of cmd/conductor-api's DTOs. Event/Phase/Kind come
// from the N-9 codegen single source (events.gen.ts) and are re-exported here so
// callers import all API types from one place.
export type { Event, Phase, Kind } from "../types/events.gen.ts";

// Project mirrors projectDTO (server.go / control.go toProjectDTO).
export interface Project {
  id: string;
  repo: string;
  base_branch: string;
  host_id: string;
  readiness: string;
  recipe_pointer: string;
  governance_policy: string;
  paused: boolean;
}

// Task mirrors taskDTO (server.go handleProjectTasks).
export interface Task {
  id: string;
  project_id: string;
  lane: string;
  tier: string;
  status: string;
  requires: string[];
  deps: string[];
  branch: string;
  scenario_id: string;
  retry_count: number;
  abort_requested: boolean;
  approved: boolean;
}

// Host mirrors hostDTO. last_heartbeat is omitted by the server when zero
// (json:",omitempty"), so it is optional here.
export interface Host {
  id: string;
  capabilities: string[];
  last_heartbeat?: string;
  heartbeat_age_seconds: number;
}

// Lease mirrors leaseDTO (server.go statusDTO.leases).
export interface Lease {
  project_id: string;
  host_id: string;
  task_id: string;
  acquired_at: string;
}

// StatusSummary mirrors statusDTO (GET /status).
export interface StatusSummary {
  projects: number;
  hosts: number;
  leases: Lease[];
  generated_at: string;
}

// IntakeResult mirrors intakeResultDTO (POST /projects/{id}/intake).
export interface IntakeResult {
  created: string[];
  skipped: string[];
}

// Scenario mirrors intake.Scenario (internal/intake/scenario.go) AS SERIALIZED BY
// the distill endpoint's distillResultDTO. The Go struct carries only `yaml:` tags,
// so encoding/json emits the Go FIELD NAMES verbatim (PascalCase) — these keys are
// therefore PascalCase, NOT snake_case. This is the RICH proposed scenario the human
// reviews before approving; HoldoutRef points at the repo-EXTERNAL hidden holdout
// (ADR-0018) and MUST NOT be a repo-relative path.
export interface Scenario {
  ID: string;
  Title: string;
  Lane: string;
  Tier: string;
  Deps: string[] | null;
  Acceptance: string[] | null;
  HoldoutRef: string;
  PublicTestRef: string;
  PublicTestsOutline: string[] | null;
  Notes: string;
}

// DistillResult mirrors distillResultDTO (POST /projects/{id}/distill): the
// structured PROPOSED scenarios for the UI to render, and the intake-ready `yaml`
// string that POST /projects/{id}/intake accepts VERBATIM. Nothing is persisted by
// distill — the human reviews/edits the yaml, then approve re-POSTs it to /intake.
export interface DistillResult {
  scenarios: Scenario[];
  yaml: string;
}

// Query params accepted by GET /events (and /ws), mirroring parseEventFilter in
// events.go: project/task/phase/kind/intervention + since/limit for history.
export interface EventQuery {
  project?: string;
  task?: string;
  phase?: string;
  kind?: string;
  intervention?: boolean;
  since?: string;
  limit?: number;
}
