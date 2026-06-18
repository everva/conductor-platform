// ProjectsTable: one row per project (id, repo, base branch, readiness, paused,
// governance policy, current lease holder). Clicking a row selects that project,
// which drives the TasksView. Renders a friendly empty state for [] projects.
//
// When a `controls` prop is supplied (3B-3) each row gains an Actions cell with
// Pause/Resume (toggled on the EFFECTIVE — optimistic — paused state) and Abort
// (gated by a confirm dialog the parent owns). The action buttons stop row-click
// propagation so acting doesn't also reselect the project. Without `controls` the
// table renders read-only (keeps the 3B-1 component tests intact).
import type { Lease, Project } from "../api/types.ts";
import type { FleetControls } from "./useFleetControls.ts";

export interface ProjectsTableProps {
  projects: Project[];
  leasesByProject: Record<string, Lease[]>;
  selectedProjectId: string | null;
  onSelect: (projectId: string) => void;
  // controls is the action layer; when omitted the Actions column is hidden.
  controls?: FleetControls;
}

function leaseHolder(leases: Lease[] | undefined): string {
  if (leases === undefined || leases.length === 0) {
    return "—";
  }
  // Surface the first lease holder compactly; multiple leases are rare per project.
  const [first] = leases;
  const extra = leases.length > 1 ? ` +${leases.length - 1}` : "";
  return `${first.host_id} · ${first.task_id}${extra}`;
}

// hasRunningTask reports whether the project holds a lease (a task is running) so
// Abort is only offered when there is something to cancel.
function hasRunningTask(leases: Lease[] | undefined): boolean {
  return leases !== undefined && leases.length > 0;
}

export function ProjectsTable({
  projects,
  leasesByProject,
  selectedProjectId,
  onSelect,
  controls,
}: ProjectsTableProps) {
  const showActions = controls !== undefined;

  return (
    <section className="fleet-panel" aria-label="Projects">
      <div className="fleet-panel-head">
        <h2>Projects</h2>
        <span className="muted">{projects.length}</span>
      </div>
      <div className="fleet-panel-body">
        {projects.length === 0 ? (
          <p className="fleet-empty">No projects onboarded.</p>
        ) : (
          <table className="fleet-table">
            <thead>
              <tr>
                <th>Project</th>
                <th>Repo</th>
                <th>Base</th>
                <th>Readiness</th>
                <th>State</th>
                <th>Governance</th>
                <th>Lease holder</th>
                {showActions && <th>Actions</th>}
              </tr>
            </thead>
            <tbody>
              {projects.map((p) => {
                const selected = p.id === selectedProjectId;
                const leases = leasesByProject[p.id];
                const paused = controls ? controls.isPaused(p.id, p.paused) : p.paused;
                const busy = controls ? controls.isProjectBusy(p.id) : false;
                return (
                  <tr
                    key={p.id}
                    className={`selectable${selected ? " selected" : ""}`}
                    aria-selected={selected}
                    onClick={() => onSelect(p.id)}
                  >
                    <td className="mono">{p.id}</td>
                    <td>{p.repo || <span className="muted">—</span>}</td>
                    <td className="mono">{p.base_branch || "—"}</td>
                    <td>{p.readiness || <span className="muted">—</span>}</td>
                    <td>
                      {paused ? (
                        <span className="badge paused">paused</span>
                      ) : (
                        <span className="muted">active</span>
                      )}
                    </td>
                    <td>{p.governance_policy || <span className="muted">—</span>}</td>
                    <td className="mono">{leaseHolder(leases)}</td>
                    {showActions && controls && (
                      <td
                        className="fleet-actions"
                        onClick={(e) => e.stopPropagation()}
                      >
                        {(() => {
                          const action = controls.projectAction(p.id);
                          // While a pause/resume is in flight, show that verb's
                          // progress (the optimistic paused bit has already flipped,
                          // so the static label alone would read the wrong way).
                          if (action === "pause") {
                            return (
                              <button type="button" className="fleet-btn" disabled>
                                Pausing…
                              </button>
                            );
                          }
                          if (action === "resume") {
                            return (
                              <button type="button" className="fleet-btn" disabled>
                                Resuming…
                              </button>
                            );
                          }
                          return paused ? (
                            <button
                              type="button"
                              className="fleet-btn"
                              disabled={busy}
                              onClick={() => controls.resume(p.id)}
                            >
                              Resume
                            </button>
                          ) : (
                            <button
                              type="button"
                              className="fleet-btn"
                              disabled={busy}
                              onClick={() => controls.pause(p.id)}
                            >
                              Pause
                            </button>
                          );
                        })()}
                        <button
                          type="button"
                          className="fleet-btn danger"
                          disabled={busy || !hasRunningTask(leases)}
                          title={
                            hasRunningTask(leases)
                              ? "Abort the running task"
                              : "No task running"
                          }
                          onClick={() => controls.requestAbort(p.id)}
                        >
                          {controls.projectAction(p.id) === "abort" ? "Aborting…" : "Abort"}
                        </button>
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
