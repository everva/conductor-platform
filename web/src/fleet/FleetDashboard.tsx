// FleetDashboard: the post-auth cockpit view (3B-1, intervention controls 3B-3). It
// owns the selected-project state and composes the data layer (useFleet) with the
// presentation panels AND the control/action layer (useFleetControls). A 401 surfaced
// by either layer is bubbled up so the app can prompt re-auth (sign-out).
//
// 3B-3 wires the four control endpoints (pause/resume/abort/approve) into the UI:
//   * a PENDING-intervention banner lifts intervention-needed signals to the top with
//     one-click Approve/Pause/Abort;
//   * the ProjectsTable rows get Pause/Resume + Abort, the TasksView gets Approve on
//     held tasks (and a project-level auto-resolve approve);
//   * Abort/Approve are confirm-gated (ConfirmDialog); actions are optimistic and
//     reconcile via useFleet.refresh(); errors surface as non-fatal notices.
import { useEffect, useState } from "react";
import { useFleet } from "./useFleet.ts";
import type { FleetClient } from "./useFleet.ts";
import { useFleetControls } from "./useFleetControls.ts";
import type { ControlClient } from "./controls.ts";
import { FleetStatusBar } from "./FleetStatusBar.tsx";
import { ProjectsTable } from "./ProjectsTable.tsx";
import { HostsPanel } from "./HostsPanel.tsx";
import { TasksView } from "./TasksView.tsx";
import { EventTicker } from "./EventTicker.tsx";
import { InterventionBanner } from "./InterventionBanner.tsx";
import { ConfirmDialog } from "./ConfirmDialog.tsx";
import { NoticeStack } from "./NoticeStack.tsx";
import { EventStreamView } from "../events/EventStreamView.tsx";
import type { HistoryLoader } from "../events/useEventFeed.ts";
import { IntakeChat } from "../intake/IntakeChat.tsx";
import type { IntakeClient } from "../intake/IntakeChat.tsx";
import { ApiClient } from "../api/client.ts";
import type { EventTransport } from "../api/useEventStream.ts";
import "./fleet.css";

export interface FleetDashboardProps {
  token: string;
  // onUnauthorized fires when the gateway rejects auth (401) so App can sign out.
  onUnauthorized: () => void;
  // makeClient is injectable for tests; defaults to the real ApiClient in useFleet.
  makeClient?: (token: string) => FleetClient;
  // makeControlClient is injectable for tests; defaults to the real ApiClient.
  makeControlClient?: (token: string) => ControlClient;
  // makeIntakeClient is injectable for tests; defaults to the real ApiClient
  // (distill + intake). The intake tab uses ONLY these two endpoints.
  makeIntakeClient?: (token: string) => IntakeClient;
  // makeHistory builds the event-history loader EventStreamView backfills from (REST
  // /events). Injectable like the others; in FORK MODE it must be the bridge ApiClient so
  // the backfill goes over the postMessage bridge — the webview CSP (connect-src 'none')
  // blocks a direct fetch, so without this the Events tab shows "Could not reach the
  // gateway". Web mode omits it → EventStreamView's default token ApiClient (unchanged).
  makeHistory?: (token: string) => HistoryLoader;
  // eventTransport, when supplied, selects FORK MODE: the host (extension) is
  // already connected, owns auth, and injects this transport (a postMessage bridge)
  // for the live event stream. Its presence means "host is connected" so the
  // dashboard mounts token-free — it forwards the transport to useFleet +
  // EventStreamView AND passes enabled: true so a token="" mount actually fetches.
  // Absent → WEB MODE: default fetch/WS transports built from the token, readiness
  // derived from token.length (today's behavior, byte-for-byte). The host pairs this
  // with transport-backed make*Client factories (e.g. () => new ApiClient({ transport }),
  // token ignored) so REST flows over the same bridge. See ADR-0027/0028.
  eventTransport?: EventTransport;
}

// DashboardTab toggles the cockpit between the fleet overview and the full event
// stream (3B-2). The fleet view keeps the lightweight EventTicker; the stream view
// is the full filterable/pausable feed.
type DashboardTab = "fleet" | "events" | "intake";

export function FleetDashboard({
  token,
  onUnauthorized,
  makeClient,
  makeControlClient,
  makeIntakeClient,
  makeHistory,
  eventTransport,
}: FleetDashboardProps) {
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null);
  const [tab, setTab] = useState<DashboardTab>("fleet");

  // The intake client defaults to the real ApiClient (distill + intake); tests
  // inject a fake. Built lazily per-render is cheap and keeps the token in memory.
  const intakeClient: IntakeClient = makeIntakeClient
    ? makeIntakeClient(token)
    : new ApiClient({ token });

  const fleet = useFleet({
    token,
    ...(selectedProjectId ? { selectedProjectId } : {}),
    ...(makeClient ? { makeClient } : {}),
    // Fork mode (eventTransport present): the host is connected, so enable a
    // token-free mount and run the live stream over the injected transport. Web
    // mode (absent): omit both — enabled defaults to token.length > 0 (unchanged).
    ...(eventTransport ? { eventTransport, enabled: true } : {}),
  });

  const controls = useFleetControls({
    token,
    refresh: fleet.refresh,
    onUnauthorized,
    ...(makeControlClient ? { makeClient: makeControlClient } : {}),
  });

  // Bubble a 401 to the app so it can return to the token gate.
  useEffect(() => {
    if (fleet.error?.unauthorized) {
      onUnauthorized();
    }
  }, [fleet.error, onUnauthorized]);

  // From an intervention-stream row, focus the project on the fleet view.
  const focusProject = (projectId: string) => {
    setSelectedProjectId(projectId);
    setTab("fleet");
  };

  return (
    <div className="fleet">
      <FleetStatusBar
        status={fleet.status}
        streamState={fleet.streamState}
        lastEventAt={fleet.lastEventAt}
        onRefresh={() => void fleet.refresh()}
      />

      <NoticeStack notices={controls.notices} onDismiss={controls.dismissNotice} />

      {fleet.error !== null && (
        <p role="alert" className="fleet-error">
          {fleet.error.message}{" "}
          <button type="button" onClick={() => void fleet.refresh()}>
            Retry
          </button>
        </p>
      )}

      <div className="fleet-tabs" role="tablist" aria-label="Dashboard view">
        <button
          type="button"
          role="tab"
          aria-selected={tab === "fleet"}
          className={tab === "fleet" ? "fleet-tab active" : "fleet-tab"}
          onClick={() => setTab("fleet")}
        >
          Fleet
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "events"}
          className={tab === "events" ? "fleet-tab active" : "fleet-tab"}
          onClick={() => setTab("events")}
        >
          Events
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === "intake"}
          className={tab === "intake" ? "fleet-tab active" : "fleet-tab"}
          onClick={() => setTab("intake")}
        >
          Intake
        </button>
      </div>

      {tab === "fleet" ? (
        <>
          <InterventionBanner
            events={fleet.recentEvents}
            controls={controls}
            onSelectProject={setSelectedProjectId}
          />
          <div className="fleet-grid">
            <div className="fleet-col">
              <ProjectsTable
                projects={fleet.projects}
                leasesByProject={fleet.leasesByProject}
                selectedProjectId={selectedProjectId}
                onSelect={setSelectedProjectId}
                controls={controls}
              />
              <TasksView
                selectedProjectId={selectedProjectId}
                tasks={
                  selectedProjectId !== null
                    ? fleet.tasksByProject[selectedProjectId]
                    : undefined
                }
                controls={controls}
              />
            </div>
            <div className="fleet-col">
              <HostsPanel hosts={fleet.hosts} />
              <EventTicker events={fleet.recentEvents} />
            </div>
          </div>
        </>
      ) : tab === "events" ? (
        <EventStreamView
          token={token}
          projects={fleet.projects.map((p) => p.id)}
          onUnauthorized={onUnauthorized}
          onInterventionAction={focusProject}
          {...(makeHistory ? { makeHistory } : {})}
          {...(eventTransport ? { eventTransport } : {})}
        />
      ) : (
        <IntakeChat
          projects={fleet.projects}
          client={intakeClient}
          onUnauthorized={onUnauthorized}
          onViewTasks={focusProject}
        />
      )}

      <ConfirmDialog
        pending={controls.pending}
        onConfirm={controls.confirm}
        onCancel={controls.cancelConfirm}
      />
    </div>
  );
}
