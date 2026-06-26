// useFleetControls: the action layer for the intervention controls (3B-3). It turns
// the four existing ApiClient control methods (pause/resume/abort/approve) into a
// safe, optimistic, confirm-gated UI surface over the live system. It owns:
//
//   * in-flight tracking (which project/task is busy) so a button can disable and
//     show "Pausing…/Aborting…/Approving…" while the POST is outstanding;
//   * optimistic + reconcile: it reflects the action immediately (e.g. paused=true)
//     for instant feedback, then calls useFleet.refresh() on success so the view
//     shows the AUTHORITATIVE store state (ADR-0025); on error it REVERTS the optimistic
//     bit and surfaces the mapped notice;
//   * confirmation: a single pending-confirm slot (Abort MUST confirm — it cancels
//     in-flight work; Approve SHOULD confirm — it triggers a real merge). Pause/Resume
//     are immediate (idempotent, low-risk);
//   * notices: a small toast/inline queue mapping ApiError → a human message (409 →
//     benign warn carrying the gateway {error}; 401 → onUnauthorized + error notice).
//
// The ControlClient is INJECTABLE so tests drive a fake without a network and never
// duplicate client logic. refresh() is supplied by the caller (useFleet.refresh).
import { useCallback, useMemo, useRef, useState } from "react";
import { ApiClient } from "../api/client.ts";
import { classifyControlError, successNotice } from "./controls.ts";
import type { ControlClient, ControlNotice } from "./controls.ts";

// ActionKind is the verb of a control action; it keys the busy state and the labels.
export type ActionKind = "pause" | "resume" | "abort" | "approve" | "retry";

// PendingConfirm describes an action awaiting the operator's explicit confirmation.
// It carries everything needed to render the dialog and run the action on confirm.
export interface PendingConfirm {
  kind: ActionKind;
  projectId: string;
  // taskId is set for a task-level approve; undefined for a project-level approve or
  // a project-scoped abort/pause/resume.
  taskId: string | undefined;
  // items, when set, makes this a BULK approve (redesign E4): each {projectId, taskId}
  // is approved+merged on confirm. The body enumerates them so the director sees the
  // exact gate-green set before one explicit confirmation (single approve leaves it unset).
  items?: { projectId: string; taskId: string }[];
  title: string;
  body: string;
  // confirmLabel is the affirmative button text (e.g. "Abort task", "Approve & merge").
  confirmLabel: string;
  // tone drives the confirm button styling: "danger" for destructive (abort).
  tone: "danger" | "primary";
}

// BusyKey identifies an outstanding action: project-scoped, or task-scoped for a
// task-level approve. We key busy state by this string so the right control disables.
function busyKey(projectId: string, taskId: string | undefined): string {
  return taskId === undefined ? `p:${projectId}` : `t:${projectId}:${taskId}`;
}

export interface FleetControls {
  // paused returns the EFFECTIVE paused state for a project: the optimistic override
  // if an action is in flight, else the authoritative store value passed in.
  isPaused: (projectId: string, storePaused: boolean) => boolean;
  // isBusy reports whether a project-scoped action (pause/resume/abort or project-level
  // approve) is currently in flight for the project.
  isProjectBusy: (projectId: string) => boolean;
  // projectAction returns the project-scoped action currently in flight (so a button
  // can show the right progress label, e.g. "Pausing…" even though the optimistic
  // paused state has already flipped), or null when idle.
  projectAction: (projectId: string) => ActionKind | null;
  // isTaskBusy reports whether a task-level approve is in flight for that task.
  isTaskBusy: (projectId: string, taskId: string) => boolean;
  // pause/resume are immediate (no confirm): optimistic flip → POST → refresh/revert.
  pause: (projectId: string) => void;
  resume: (projectId: string) => void;
  // retry re-queues a BLOCKED task (blocked→ready) so the agent picks it up again. Direct
  // (no confirm): non-destructive, and the gateway clears the stale block reason on the re-run.
  retry: (projectId: string, taskId: string) => void;
  // requestAbort / requestApprove OPEN the confirm dialog (they do not act yet).
  requestAbort: (projectId: string) => void;
  // requestApprove with a taskId is task-level; without it is project-level auto-resolve.
  requestApprove: (projectId: string, taskId?: string) => void;
  // requestBulkApprove OPENS one confirm enumerating every selected gate-green task;
  // on confirm each is approved+merged. A no-op for an empty selection (redesign E4).
  requestBulkApprove: (items: { projectId: string; taskId: string }[]) => void;
  // the pending confirmation (null when none); confirm runs it, cancel dismisses it.
  pending: PendingConfirm | null;
  confirm: () => void;
  cancelConfirm: () => void;
  // notices is the active non-fatal message queue (newest last); dismiss removes one.
  notices: ControlNotice[];
  dismissNotice: (id: number) => void;
}

export interface UseFleetControlsOptions {
  token: string;
  // refresh re-reads the authoritative store snapshot (useFleet.refresh).
  refresh: () => Promise<void>;
  // onUnauthorized fires on a 401 so the app can sign out (same contract as useFleet).
  onUnauthorized: () => void;
  // makeClient builds the control client (default: real ApiClient). Injectable for tests.
  makeClient?: (token: string) => ControlClient;
}

export function useFleetControls(opts: UseFleetControlsOptions): FleetControls {
  const {
    token,
    refresh,
    onUnauthorized,
    makeClient = (t) => new ApiClient({ token: t }),
  } = opts;

  const client = useMemo(() => makeClient(token), [makeClient, token]);

  // busy maps an in-flight action key → the action verb (so a button can label its
  // progress correctly); pausedOverride holds the optimistic paused bit per project
  // while a pause/resume POST is outstanding.
  const [busy, setBusy] = useState<Record<string, ActionKind>>({});
  const [pausedOverride, setPausedOverride] = useState<Record<string, boolean>>({});
  const [pending, setPending] = useState<PendingConfirm | null>(null);
  const [notices, setNotices] = useState<ControlNotice[]>([]);
  const noticeSeq = useRef(0);

  const pushNotice = useCallback((n: Omit<ControlNotice, "id">) => {
    noticeSeq.current += 1;
    const id = noticeSeq.current;
    setNotices((prev) => [...prev, { ...n, id }]);
  }, []);

  const dismissNotice = useCallback((id: number) => {
    setNotices((prev) => prev.filter((n) => n.id !== id));
  }, []);

  // handleOutcome routes a classified error: 401 → sign out; otherwise surface the
  // mapped notice. Returns true when the action ultimately succeeded path should run.
  const handleError = useCallback(
    (err: unknown) => {
      const { unauthorized, notice } = classifyControlError(err);
      pushNotice(notice);
      if (unauthorized) {
        onUnauthorized();
      }
    },
    [onUnauthorized, pushNotice],
  );

  // setPauseInFlight toggles an optimistic paused bit + busy marker for a project.
  const togglePause = useCallback(
    (projectId: string, target: boolean) => {
      const key = busyKey(projectId, undefined);
      setBusy((b) => ({ ...b, [key]: target ? "pause" : "resume" }));
      setPausedOverride((o) => ({ ...o, [projectId]: target }));
      const call = target ? client.pause(projectId) : client.resume(projectId);
      void call
        .then(async () => {
          await refresh();
          pushNotice(
            successNotice(target ? `Paused ${projectId}.` : `Resumed ${projectId}.`),
          );
        })
        .catch((err: unknown) => {
          // Revert the optimistic paused bit so the view snaps back to the truth.
          setPausedOverride((o) => ({ ...o, [projectId]: !target }));
          handleError(err);
        })
        .finally(() => {
          setBusy((b) => {
            const next = { ...b };
            delete next[key];
            return next;
          });
          // Clear the override so the authoritative store value (post-refresh) wins.
          setPausedOverride((o) => {
            const next = { ...o };
            delete next[projectId];
            return next;
          });
        });
    },
    [client, refresh, pushNotice, handleError],
  );

  const pause = useCallback((projectId: string) => togglePause(projectId, true), [
    togglePause,
  ]);
  const resume = useCallback((projectId: string) => togglePause(projectId, false), [
    togglePause,
  ]);

  // runAbort / runApprove execute AFTER the operator confirms. They mark busy, POST,
  // refresh on success (store reflection), and surface the mapped notice on error.
  const runAbort = useCallback(
    (projectId: string) => {
      const key = busyKey(projectId, undefined);
      setBusy((b) => ({ ...b, [key]: "abort" }));
      void client
        .abort(projectId)
        .then(async (res) => {
          await refresh();
          pushNotice(successNotice(`Aborting ${res.aborted_task} on ${projectId}.`));
        })
        .catch(handleError)
        .finally(() => {
          setBusy((b) => {
            const next = { ...b };
            delete next[key];
            return next;
          });
        });
    },
    [client, refresh, pushNotice, handleError],
  );

  const runApprove = useCallback(
    (projectId: string, taskId: string | undefined) => {
      const key = busyKey(projectId, taskId);
      setBusy((b) => ({ ...b, [key]: "approve" }));
      void client
        .approve(projectId, taskId)
        .then(async (res) => {
          await refresh();
          pushNotice(successNotice(`Approved ${res.approved_task} on ${projectId}.`));
        })
        .catch(handleError)
        .finally(() => {
          setBusy((b) => {
            const next = { ...b };
            delete next[key];
            return next;
          });
        });
    },
    [client, refresh, pushNotice, handleError],
  );

  // retry is a DIRECT task action (no confirm): re-queue a blocked task, refresh, notice.
  const retry = useCallback(
    (projectId: string, taskId: string) => {
      const key = busyKey(projectId, taskId);
      setBusy((b) => ({ ...b, [key]: "retry" }));
      void client
        .retry(projectId, taskId)
        .then(async (res) => {
          await refresh();
          pushNotice(successNotice(`Retrying ${res.task} on ${projectId}.`));
        })
        .catch(handleError)
        .finally(() => {
          setBusy((b) => {
            const next = { ...b };
            delete next[key];
            return next;
          });
        });
    },
    [client, refresh, pushNotice, handleError],
  );

  const requestAbort = useCallback((projectId: string) => {
    setPending({
      kind: "abort",
      projectId,
      taskId: undefined,
      title: "Abort the running task?",
      body: `This cancels the in-flight task on ${projectId}. The daemon stops it on its next tick.`,
      confirmLabel: "Abort task",
      tone: "danger",
    });
  }, []);

  const requestApprove = useCallback((projectId: string, taskId?: string) => {
    setPending({
      kind: "approve",
      projectId,
      taskId,
      title: taskId !== undefined ? `Approve task ${taskId}?` : "Approve awaiting task?",
      body:
        taskId !== undefined
          ? `Approving ${taskId} releases the human-hold and triggers a real merge on the next tick.`
          : `Approving the unique awaiting task on ${projectId} triggers a real merge on the next tick.`,
      confirmLabel: "Approve & merge",
      tone: "primary",
    });
  }, []);

  const requestBulkApprove = useCallback(
    (items: { projectId: string; taskId: string }[]) => {
      if (items.length === 0) {
        return;
      }
      // Enumerate so nothing is hidden; cap the visible list to keep the modal sane
      // (the count in the title stays authoritative).
      const shown = items.slice(0, 15).map((i) => `${i.projectId}/${i.taskId}`);
      const more = items.length - shown.length;
      const list = shown.join(", ") + (more > 0 ? `, and ${more} more` : "");
      setPending({
        kind: "approve",
        projectId: items[0]!.projectId,
        taskId: undefined,
        items,
        title: `Approve ${items.length} task${items.length === 1 ? "" : "s"}?`,
        body: `Each of these gate-green tasks triggers a real merge on the next tick: ${list}.`,
        confirmLabel: `Approve & merge ${items.length}`,
        tone: "primary",
      });
    },
    [],
  );

  const confirm = useCallback(() => {
    if (pending === null) {
      return;
    }
    const p = pending;
    setPending(null);
    if (p.kind === "abort") {
      runAbort(p.projectId);
    } else if (p.kind === "approve") {
      if (p.items && p.items.length > 0) {
        // Bulk: fire each task-level approve (each tracked + reconciled on its own).
        for (const it of p.items) {
          runApprove(it.projectId, it.taskId);
        }
      } else {
        runApprove(p.projectId, p.taskId);
      }
    }
  }, [pending, runAbort, runApprove]);

  const cancelConfirm = useCallback(() => setPending(null), []);

  const isPaused = useCallback(
    (projectId: string, storePaused: boolean): boolean => {
      const override = pausedOverride[projectId];
      return override === undefined ? storePaused : override;
    },
    [pausedOverride],
  );

  const isProjectBusy = useCallback(
    (projectId: string): boolean => busy[busyKey(projectId, undefined)] !== undefined,
    [busy],
  );

  const projectAction = useCallback(
    (projectId: string): ActionKind | null =>
      busy[busyKey(projectId, undefined)] ?? null,
    [busy],
  );

  const isTaskBusy = useCallback(
    (projectId: string, taskId: string): boolean =>
      busy[busyKey(projectId, taskId)] !== undefined,
    [busy],
  );

  return {
    isPaused,
    isProjectBusy,
    projectAction,
    isTaskBusy,
    pause,
    resume,
    retry,
    requestAbort,
    requestApprove,
    requestBulkApprove,
    pending,
    confirm,
    cancelConfirm,
    notices,
    dismissNotice,
  };
}
