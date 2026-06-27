// Shared control-surface plumbing for the intervention controls (3B-3). It defines
// the narrow ControlClient the action layer consumes (a subset of ApiClient's
// control methods — ApiClient satisfies it, tests pass a fake), the typed
// notice/error mapping for control responses, and the pure helper that decides
// which tasks of a project are HELD awaiting a human (ADR-0003 T3/T4 human-gate).
//
// These are the EXACT existing ApiClient control methods (ADR-0025 store
// reflection: POST → gateway → store; the daemon acts on its next tick). We do NOT
// add a second HTTP path — the action layer reflects the store via useFleet.refresh().
import { ApiError } from "../api/client.ts";
import type { Task } from "../api/types.ts";

// AWAITING_APPROVAL_STATUS mirrors internal/conductor.StatusAwaitingApproval — a
// task HELD by governance awaiting a human approval (the merge-gate, ADR-0003).
export const AWAITING_APPROVAL_STATUS = "awaiting-approval";

// ControlClient is the narrow mutation surface the controls consume. ApiClient
// already implements every method with these exact shapes (api/client.ts); a fake
// in tests implements only these so no network and no duplicated client logic.
export interface ControlClient {
  pause(projectId: string): Promise<{ project: string; paused: boolean }>;
  resume(projectId: string): Promise<{ project: string; paused: boolean }>;
  abort(projectId: string): Promise<{ project: string; aborted_task: string }>;
  approve(
    projectId: string,
    taskId?: string,
  ): Promise<{ project: string; approved_task: string }>;
  // reject DECLINES a held awaiting-approval task (counterpart to approve): it moves to the
  // terminal "rejected" status WITHOUT merging. reason is an optional short note for the board.
  reject(
    projectId: string,
    taskId?: string,
    reason?: string,
  ): Promise<{ project: string; rejected_task: string }>;
  retry(
    projectId: string,
    taskId: string,
  ): Promise<{ project: string; task: string; status: string }>;
}

// ControlNotice is a one-shot, non-fatal message surfaced after an action. `tone`
// drives the colour: "ok" for a confirmed mutation, "warn" for a benign 409 (e.g.
// "no task running to abort"), "error" for an unexpected failure.
export interface ControlNotice {
  id: number;
  tone: "ok" | "warn" | "error";
  message: string;
}

// ControlOutcome is the classified result of an attempted action: whether it was a
// 401 (caller must sign out), and the human notice to surface. A 409 from the
// gateway (nothing-running / none-or-ambiguous-awaiting) is NOT a failure of the UI
// — it is a benign "nothing to do" surfaced as a warn notice, never a stuck spinner.
export interface ControlOutcome {
  unauthorized: boolean;
  notice: Omit<ControlNotice, "id">;
}

// isAwaitingApproval reports whether a task is HELD awaiting a human approval: the
// gateway parks it in the awaiting-approval status and it has NOT yet been approved.
// Either signal alone is treated as awaiting (defensive: a held task is the unit a
// human resolves) so the Approve affordance never hides a genuinely-held task.
export function isAwaitingApproval(task: Task): boolean {
  return task.status === AWAITING_APPROVAL_STATUS && !task.approved;
}

// awaitingTasks returns the project's tasks that are awaiting approval, stably by id.
export function awaitingTasks(tasks: Task[] | undefined): Task[] {
  if (tasks === undefined) {
    return [];
  }
  return tasks
    .filter(isAwaitingApproval)
    .sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

// successNotice builds the confirmed-mutation notice for an action verb.
export function successNotice(message: string): Omit<ControlNotice, "id"> {
  return { tone: "ok", message };
}

// classifyControlError maps a thrown ApiError (or unknown) to a ControlOutcome. A
// 401 flags unauthorized (the app signs out). A 409 carries the gateway's secret-free
// {error} message verbatim as a benign warn (nothing-running / none-or-ambiguous
// awaiting). Anything else is a clear error notice. It NEVER throws and never leaks
// the token (ApiError already strips it).
export function classifyControlError(err: unknown): ControlOutcome {
  if (err instanceof ApiError) {
    if (err.status === 401) {
      return {
        unauthorized: true,
        notice: { tone: "error", message: "Authentication failed." },
      };
    }
    if (err.status === 409) {
      // Benign conflict: nothing to act on. Surface the gateway message as-is.
      return { unauthorized: false, notice: { tone: "warn", message: err.message } };
    }
    return {
      unauthorized: false,
      notice: { tone: "error", message: `Gateway error (${err.status}): ${err.message}` },
    };
  }
  return {
    unauthorized: false,
    notice: { tone: "error", message: "Could not reach the gateway." },
  };
}
