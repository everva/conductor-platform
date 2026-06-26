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
  // reason is the task's LastError — WHY it last went non-pass (blocked / needs-user).
  // Empty for healthy tasks; the board renders it on a stalled card so the director sees
  // the cause instead of a bare "blocked". Optional: older gateways omit it.
  reason?: string;
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

// Scenario mirrors the distill endpoint's scenarioDTO (cmd/conductor-api/control.go):
// a snake_case PROPOSED scenario the human reviews before approving. hidden_holdout_ref
// points at the repo-EXTERNAL hidden holdout (ADR-0018) and MUST NOT be a repo-relative
// path. (The gateway emits snake_case to match every other DTO; review sweep S-1.)
export interface Scenario {
  id: string;
  title: string;
  lane: string;
  tier: string;
  deps: string[];
  acceptance: string[];
  hidden_holdout_ref: string;
}

// QuestionOption is one offered choice of a clarifying Question (label + a
// description of what choosing it means), mirroring questionOptionDTO (ADR-0047).
export interface QuestionOption {
  label: string;
  description: string;
}

// Question is a clarifying question the distiller asks when the conversation is too
// ambiguous to distill into scenarios — the mirror of Claude Code's AskUserQuestion
// (ADR-0047). The UI renders `header` as a short chip and ADDS an "Other" free-text
// choice automatically; `multi_select` allows more than one option to be picked.
export interface Question {
  question: string;
  header: string;
  options: QuestionOption[];
  multi_select: boolean;
}

// DistillResult mirrors distillResultDTO (POST /projects/{id}/distill): the
// structured PROPOSED scenarios for the UI to render, and the intake-ready `yaml`
// string that POST /projects/{id}/intake accepts VERBATIM. Nothing is persisted by
// distill — the human reviews/edits the yaml, then approve re-POSTs it to /intake.
export interface DistillResult {
  scenarios: Scenario[];
  yaml: string;
  // holdout is the OPTIONAL auto-generated hidden-holdout test (Faz-S S4): path -> file content,
  // for the director to REVIEW before approving. Present only when the distiller emitted one. On
  // approve the editor PUTs it to /holdouts/{id} so the gate can run it (S5).
  holdout?: Record<string, string>;
  // questions is the ADDITIVE clarifying turn (ADR-0047): when present, the distiller
  // asked for more detail instead of proposing scenarios (scenarios is then empty and
  // yaml is ""). Absent on a scenarios result, so legacy consumers are unaffected.
  questions?: Question[];
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
