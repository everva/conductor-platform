// TasksView: the tasks of the currently selected project (id, lane, tier, status,
// deps, branch, and the abort_requested / approved flags as badges). Rows are
// stably sorted by task id. Renders distinct empty states for "no project selected"
// vs "project has no tasks".
//
// When a `controls` prop is supplied (3B-3) a task HELD awaiting a human approval
// (status awaiting-approval, not yet approved — ADR-0003 T3/T4 merge-gate) gains an
// Approve button (task-level approve, confirm-gated). When the project has exactly
// one such task a project-level "Approve awaiting" affordance is also offered in the
// panel head (auto-resolve, no task id). Without `controls` the view is read-only
// (keeps the 3B-1 component tests intact).
import type { Task } from "../api/types.ts";
import { awaitingTasks, isAwaitingApproval } from "./controls.ts";
import type { FleetControls } from "./useFleetControls.ts";

export interface TasksViewProps {
  selectedProjectId: string | null;
  tasks: Task[] | undefined;
  // controls is the action layer; when omitted no Approve affordances are shown.
  controls?: FleetControls;
}

function byId(a: Task, b: Task): number {
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

export function TasksView({ selectedProjectId, tasks, controls }: TasksViewProps) {
  const sorted = tasks === undefined ? [] : [...tasks].sort(byId);
  const awaiting = awaitingTasks(tasks);
  // Project-level approve auto-resolves a UNIQUE awaiting task; only offer it then.
  const canProjectApprove =
    controls !== undefined && selectedProjectId !== null && awaiting.length === 1;

  return (
    <section className="fleet-panel" aria-label="Tasks">
      <div className="fleet-panel-head">
        <h2>Tasks{selectedProjectId !== null ? ` · ${selectedProjectId}` : ""}</h2>
        <span className="fleet-head-right">
          {canProjectApprove && controls && selectedProjectId !== null && (
            <button
              type="button"
              className="fleet-btn primary"
              disabled={controls.isProjectBusy(selectedProjectId)}
              onClick={() => controls.requestApprove(selectedProjectId)}
            >
              Approve awaiting
            </button>
          )}
          <span className="muted">{sorted.length}</span>
        </span>
      </div>
      <div className="fleet-panel-body">
        {selectedProjectId === null ? (
          <p className="fleet-empty">Select a project to view its tasks.</p>
        ) : sorted.length === 0 ? (
          <p className="fleet-empty">No tasks for this project.</p>
        ) : (
          <table className="fleet-table">
            <thead>
              <tr>
                <th>Task</th>
                <th>Lane</th>
                <th>Tier</th>
                <th>Status</th>
                <th>Deps</th>
                <th>Branch</th>
                <th>Flags</th>
                {controls && <th>Actions</th>}
              </tr>
            </thead>
            <tbody>
              {sorted.map((t) => {
                const held = isAwaitingApproval(t);
                const busy =
                  controls !== undefined && controls.isTaskBusy(t.project_id, t.id);
                return (
                  <tr key={t.id} className={held ? "awaiting" : undefined}>
                    <td className="mono">{t.id}</td>
                    <td>{t.lane || <span className="muted">—</span>}</td>
                    <td>{t.tier || <span className="muted">—</span>}</td>
                    <td>
                      {held ? (
                        <span className="badge awaiting">awaiting-approval</span>
                      ) : (
                        t.status || <span className="muted">—</span>
                      )}
                      {t.status === "blocked" && t.reason ? (
                        <div className="task-reason" title={t.reason}>
                          {t.reason}
                        </div>
                      ) : null}
                    </td>
                    <td className="mono">
                      {t.deps.length === 0 ? (
                        <span className="muted">—</span>
                      ) : (
                        t.deps.join(", ")
                      )}
                    </td>
                    <td className="mono">
                      {t.branch || <span className="muted">—</span>}
                    </td>
                    <td>
                      {t.approved && <span className="badge approved">approved</span>}
                      {t.abort_requested && (
                        <span className="badge abort" style={{ marginLeft: "0.3rem" }}>
                          abort
                        </span>
                      )}
                      {!t.approved && !t.abort_requested && (
                        <span className="muted">—</span>
                      )}
                    </td>
                    {controls && (
                      <td
                        className="fleet-actions"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {held ? (
                          <button
                            type="button"
                            className="fleet-btn primary"
                            disabled={busy}
                            onClick={() =>
                              controls.requestApprove(t.project_id, t.id)
                            }
                          >
                            {busy ? "Approving…" : "Approve"}
                          </button>
                        ) : t.status === "blocked" ? (
                          <button
                            type="button"
                            className="fleet-btn primary"
                            disabled={busy}
                            onClick={() => controls.retry(t.project_id, t.id)}
                          >
                            {busy ? "Retrying…" : "Retry"}
                          </button>
                        ) : t.status === "running" ? (
                          <button
                            type="button"
                            className="fleet-btn danger"
                            disabled={controls.isProjectBusy(t.project_id)}
                            onClick={() => controls.requestAbort(t.project_id)}
                          >
                            {controls.isProjectBusy(t.project_id)
                              ? "Stopping…"
                              : "Stop"}
                          </button>
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}
