// FleetDashboard: the post-auth cockpit view (3B-1). It owns the selected-project
// state and composes the data layer (useFleet) with the presentation panels. A 401
// surfaced by useFleet is bubbled up so the app can prompt re-auth (sign-out).
import { useEffect, useState } from "react";
import { useFleet } from "./useFleet.ts";
import type { FleetClient } from "./useFleet.ts";
import { FleetStatusBar } from "./FleetStatusBar.tsx";
import { ProjectsTable } from "./ProjectsTable.tsx";
import { HostsPanel } from "./HostsPanel.tsx";
import { TasksView } from "./TasksView.tsx";
import { EventTicker } from "./EventTicker.tsx";
import "./fleet.css";

export interface FleetDashboardProps {
  token: string;
  // onUnauthorized fires when the gateway rejects auth (401) so App can sign out.
  onUnauthorized: () => void;
  // makeClient is injectable for tests; defaults to the real ApiClient in useFleet.
  makeClient?: (token: string) => FleetClient;
}

export function FleetDashboard({
  token,
  onUnauthorized,
  makeClient,
}: FleetDashboardProps) {
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null);

  const fleet = useFleet({
    token,
    ...(selectedProjectId ? { selectedProjectId } : {}),
    ...(makeClient ? { makeClient } : {}),
  });

  // Bubble a 401 to the app so it can return to the token gate.
  useEffect(() => {
    if (fleet.error?.unauthorized) {
      onUnauthorized();
    }
  }, [fleet.error, onUnauthorized]);

  return (
    <div className="fleet">
      <FleetStatusBar
        status={fleet.status}
        streamState={fleet.streamState}
        lastEventAt={fleet.lastEventAt}
        onRefresh={() => void fleet.refresh()}
      />

      {fleet.error !== null && (
        <p role="alert" className="fleet-error">
          {fleet.error.message}{" "}
          <button type="button" onClick={() => void fleet.refresh()}>
            Retry
          </button>
        </p>
      )}

      <div className="fleet-grid">
        <div className="fleet-col">
          <ProjectsTable
            projects={fleet.projects}
            leasesByProject={fleet.leasesByProject}
            selectedProjectId={selectedProjectId}
            onSelect={setSelectedProjectId}
          />
          <TasksView
            selectedProjectId={selectedProjectId}
            tasks={
              selectedProjectId !== null
                ? fleet.tasksByProject[selectedProjectId]
                : undefined
            }
          />
        </div>
        <div className="fleet-col">
          <HostsPanel hosts={fleet.hosts} />
          <EventTicker events={fleet.recentEvents} />
        </div>
      </div>
    </div>
  );
}
