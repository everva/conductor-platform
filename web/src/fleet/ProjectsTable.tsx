// ProjectsTable: one row per project (id, repo, base branch, readiness, paused,
// governance policy, current lease holder). Clicking a row selects that project,
// which drives the TasksView. Renders a friendly empty state for [] projects.
import type { Lease, Project } from "../api/types.ts";

export interface ProjectsTableProps {
  projects: Project[];
  leasesByProject: Record<string, Lease[]>;
  selectedProjectId: string | null;
  onSelect: (projectId: string) => void;
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

export function ProjectsTable({
  projects,
  leasesByProject,
  selectedProjectId,
  onSelect,
}: ProjectsTableProps) {
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
              </tr>
            </thead>
            <tbody>
              {projects.map((p) => {
                const selected = p.id === selectedProjectId;
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
                      {p.paused ? (
                        <span className="badge paused">paused</span>
                      ) : (
                        <span className="muted">active</span>
                      )}
                    </td>
                    <td>{p.governance_policy || <span className="muted">—</span>}</td>
                    <td className="mono">{leaseHolder(leasesByProject[p.id])}</td>
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
