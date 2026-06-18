// TasksView: the tasks of the currently selected project (id, lane, tier, status,
// deps, branch, and the abort_requested / approved flags as badges). Rows are
// stably sorted by task id. Renders distinct empty states for "no project selected"
// vs "project has no tasks".
import type { Task } from "../api/types.ts";

export interface TasksViewProps {
  selectedProjectId: string | null;
  tasks: Task[] | undefined;
}

function byId(a: Task, b: Task): number {
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

export function TasksView({ selectedProjectId, tasks }: TasksViewProps) {
  const sorted = tasks === undefined ? [] : [...tasks].sort(byId);

  return (
    <section className="fleet-panel" aria-label="Tasks">
      <div className="fleet-panel-head">
        <h2>Tasks{selectedProjectId !== null ? ` · ${selectedProjectId}` : ""}</h2>
        <span className="muted">{sorted.length}</span>
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
              </tr>
            </thead>
            <tbody>
              {sorted.map((t) => (
                <tr key={t.id}>
                  <td className="mono">{t.id}</td>
                  <td>{t.lane || <span className="muted">—</span>}</td>
                  <td>{t.tier || <span className="muted">—</span>}</td>
                  <td>{t.status || <span className="muted">—</span>}</td>
                  <td className="mono">
                    {t.deps.length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      t.deps.join(", ")
                    )}
                  </td>
                  <td className="mono">{t.branch || <span className="muted">—</span>}</td>
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
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </section>
  );
}
