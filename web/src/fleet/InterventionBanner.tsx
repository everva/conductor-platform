// InterventionBanner: a contextual, one-click action surface for PENDING human
// interventions (3B-3). When the live event feed carries an `intervention-needed`
// event (ADR-0011 human-gate signal), the operator should not have to hunt through
// the projects/tasks tables to act — this banner lifts the most recent intervention
// per project to the top of the fleet view and offers the relevant safe action
// inline: Approve (release the human-hold / merge), Pause, or Abort.
//
// The actions reuse the SAME confirm-gated control layer as the tables
// (useFleetControls): Approve/Abort open the confirm dialog; Pause is immediate. It
// is derived state only — it reads the recent WS events and renders nothing when no
// intervention is pending.
import { useMemo } from "react";
import { KIND_INTERVENTION_NEEDED } from "../types/events.gen.ts";
import type { Event } from "../api/types.ts";
import type { FleetControls } from "./useFleetControls.ts";

export interface InterventionBannerProps {
  // events is the recent WS buffer (oldest→newest) from useFleet.recentEvents.
  events: Event[];
  controls: FleetControls;
  // onSelectProject focuses the project in the tables (so the operator can drill in).
  onSelectProject: (projectId: string) => void;
}

// PendingIntervention is the most-recent intervention-needed signal for a project.
interface PendingIntervention {
  project: string;
  task: string;
  ts: string;
}

// pendingInterventions reduces the event buffer to the LATEST intervention per
// project (events are ascending by arrival, so a later one overwrites an earlier).
function pendingInterventions(events: Event[]): PendingIntervention[] {
  const byProject = new Map<string, PendingIntervention>();
  for (const e of events) {
    if (e.kind === KIND_INTERVENTION_NEEDED && e.project) {
      byProject.set(e.project, { project: e.project, task: e.task, ts: e.ts });
    }
  }
  return [...byProject.values()].sort((a, b) =>
    a.project < b.project ? -1 : a.project > b.project ? 1 : 0,
  );
}

export function InterventionBanner({
  events,
  controls,
  onSelectProject,
}: InterventionBannerProps) {
  const pending = useMemo(() => pendingInterventions(events), [events]);

  if (pending.length === 0) {
    return null;
  }

  return (
    <div className="fleet-intervention" role="alert" aria-label="Pending interventions">
      <div className="fleet-intervention-head">
        <span className="fleet-intervention-mark">⚠ Intervention needed</span>
        <span className="muted">{pending.length} pending</span>
      </div>
      <ul className="fleet-intervention-list">
        {pending.map((p) => {
          const busy = controls.isProjectBusy(p.project);
          return (
            <li key={p.project} className="fleet-intervention-row">
              <button
                type="button"
                className="fleet-intervention-loc mono"
                onClick={() => onSelectProject(p.project)}
                title="Show this project"
              >
                {p.project}
                {p.task ? `·${p.task}` : ""}
              </button>
              <span className="fleet-intervention-actions fleet-actions">
                <button
                  type="button"
                  className="fleet-btn primary"
                  disabled={busy}
                  onClick={() =>
                    controls.requestApprove(
                      p.project,
                      p.task.length > 0 ? p.task : undefined,
                    )
                  }
                >
                  Approve
                </button>
                <button
                  type="button"
                  className="fleet-btn"
                  disabled={busy}
                  onClick={() => controls.pause(p.project)}
                >
                  {busy ? "Pausing…" : "Pause"}
                </button>
                <button
                  type="button"
                  className="fleet-btn danger"
                  disabled={busy}
                  onClick={() => controls.requestAbort(p.project)}
                >
                  Abort
                </button>
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
