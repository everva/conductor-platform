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
import { useEffect, useMemo, useRef, useState } from "react";
import { useFleet } from "./useFleet.ts";
import type { FleetClient } from "./useFleet.ts";
import { useFleetControls } from "./useFleetControls.ts";
import type { ControlClient } from "./controls.ts";
import { FleetStatusBar } from "./FleetStatusBar.tsx";
import { CommandCenter } from "./CommandCenter.tsx";
import { CommandPalette } from "./CommandPalette.tsx";
import type { PaletteAction } from "./palette.ts";
import { NeedsReviewBadge } from "./NeedsReviewBadge.tsx";
import { needsReviewCount } from "./board.ts";
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
import { SessionView } from "../session/SessionView.tsx";
import type { ScenarioClient } from "../session/SessionView.tsx";
import { ApiClient } from "../api/client.ts";
import type { Task } from "../api/types.ts";
import type { EventTransport } from "../api/useEventStream.ts";
import { Activity, Inbox, LayoutGrid, Search, Server } from "lucide-react";
import type { LucideIcon } from "lucide-react";
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
  // makeScenarioClient builds the read client the session view fetches a task's spec
  // (acceptance) from (GET /projects/{id}/scenarios). Injectable like the others;
  // defaults to the real ApiClient. In FORK MODE the host passes a bridge-backed one.
  makeScenarioClient?: (token: string) => ScenarioClient;
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
  // selection, when set (FORK MODE / Faz-Q), is the host-owned selection pushed onto the cockpit
  // (the native sessions tree OWNS it; this view is a projection). `task` omitted → select the
  // project (scope + show the board); `task` present → ALSO open that task's SessionView — the SAME
  // selectedTask seam a board drill-in uses (the former N3 deep-link). The fork passes a NEW object
  // per host request (even for the same ids) so a repeat selection re-fires. Web mode omits it.
  selection?: { project: string; task?: string } | null;
}

// DashboardTab selects the cockpit surface. "board" is the agent-native Command
// Center (redesign E1) and the DEFAULT surface a director lands on; "fleet" is the
// per-project detail (the 3B overview, kept as a drill-in), "events" the full
// filterable feed (3B-2), "intake" the spec/distill flow.
type DashboardTab = "board" | "fleet" | "events" | "intake";

const TABS: { key: DashboardTab; label: string; Icon: LucideIcon }[] = [
  { key: "board", label: "Board", Icon: LayoutGrid },
  { key: "fleet", label: "Fleet", Icon: Server },
  { key: "events", label: "Events", Icon: Activity },
  { key: "intake", label: "Intake", Icon: Inbox },
];

export function FleetDashboard({
  token,
  onUnauthorized,
  makeClient,
  makeControlClient,
  makeIntakeClient,
  makeHistory,
  makeScenarioClient,
  eventTransport,
  selection,
}: FleetDashboardProps) {
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null);
  // selectedTask drives the session detail view (redesign E2): set by a board card
  // drill-in; cleared by the session's back button. When set it replaces the tabbed
  // content with the SessionView (the status bar + notices + confirm dialog persist).
  const [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const [tab, setTab] = useState<DashboardTab>("board");
  // The ⌘K command palette (redesign E4): a keyboard-first overlay to jump to any
  // session, switch surface, or start new work. Opened globally by ⌘K/Ctrl+K (or
  // the tab-bar trigger) and reachable from the session view too.
  const [paletteOpen, setPaletteOpen] = useState(false);

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

  // Selection (Faz-Q / Q0): the host (native sessions tree) OWNS the selection and pushes it here.
  // `selection.task` present → open that task's SessionView, resolved against the live fleet (the
  // SAME seam a board drill-in uses, the former N3 deep-link); `selection.task` absent → select the
  // PROJECT: scope to it + show the board, leaving any open session. Consumed once per distinct
  // request object (a useRef guard), so it fires once and does NOT re-fire after the user backs out;
  // a task not yet loaded waits for the next fleet update (the effect also depends on tasksByProject)
  // and resolves then. Web mode omits the prop.
  const consumedSel = useRef<{ project: string; task?: string } | null>(null);
  useEffect(() => {
    if (!selection || selection === consumedSel.current) {
      return;
    }
    if (selection.task === undefined) {
      // Project-only select: scope to the project + show the board (the overview surface).
      consumedSel.current = selection;
      setSelectedProjectId(selection.project);
      setSelectedTask(null);
      setTab("board");
      return;
    }
    const task = (fleet.tasksByProject[selection.project] ?? []).find(
      (t) => t.id === selection.task,
    );
    if (!task) {
      return; // wait for fleet data; the effect re-runs when tasksByProject changes
    }
    consumedSel.current = selection;
    setSelectedProjectId(selection.project);
    setSelectedTask(task);
  }, [selection, fleet.tasksByProject]);

  // ⌘K / Ctrl+K toggles the command palette from anywhere in the cockpit (board,
  // a tab, or a session). A window-level listener keeps the shortcut working
  // regardless of which control holds focus.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        setPaletteOpen((open) => !open);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // runPaletteAction maps a palette selection to the cockpit's existing navigation
  // seams (the same setTab/setSelectedTask a tab click or board drill-in uses) and
  // closes the palette. Navigation clears any open session so the chosen surface
  // is what renders.
  const runPaletteAction = (action: PaletteAction) => {
    setPaletteOpen(false);
    switch (action.type) {
      case "navigate":
        setSelectedTask(null);
        setTab(action.surface);
        break;
      case "new-work":
        setSelectedTask(null);
        setTab("intake");
        break;
      case "open-session":
        setSelectedTask(action.task);
        break;
    }
  };

  // The persistent review signal (redesign E4): how many tasks await the director
  // right now, by the SAME bucketing the board's Needs-Review column uses. Surfaced
  // shell-wide (every surface, incl. inside a session) so review work is never out
  // of sight; the badge routes back to the board's review lane in one click.
  const reviewCount = useMemo(
    () => needsReviewCount(fleet.tasksByProject, fleet.leasesByProject),
    [fleet.tasksByProject, fleet.leasesByProject],
  );
  const goReview = () => {
    setSelectedTask(null);
    setTab("board");
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

      <NeedsReviewBadge count={reviewCount} onReview={goReview} />

      {fleet.error !== null && (
        <p role="alert" className="fleet-error">
          {fleet.error.message}{" "}
          <button type="button" onClick={() => void fleet.refresh()}>
            Retry
          </button>
        </p>
      )}

      {selectedTask ? (
        <SessionView
          task={selectedTask}
          token={token}
          onBack={() => setSelectedTask(null)}
          onUnauthorized={onUnauthorized}
          controls={controls}
          {...(makeScenarioClient ? { makeScenarioClient } : {})}
          {...(makeHistory ? { makeHistory } : {})}
          {...(eventTransport ? { eventTransport } : {})}
        />
      ) : (
        <>
      <div className="fleet-tabbar">
      <div className="fleet-tabs" role="tablist" aria-label="Dashboard view">
        {TABS.map(({ key, label, Icon }) => (
          <button
            key={key}
            type="button"
            role="tab"
            aria-selected={tab === key}
            className={tab === key ? "fleet-tab active" : "fleet-tab"}
            onClick={() => setTab(key)}
          >
            <Icon size={14} strokeWidth={2.2} aria-hidden="true" />
            {label}
          </button>
        ))}
      </div>
      <button
        type="button"
        className="cmdk-trigger"
        aria-keyshortcuts="Meta+K Control+K"
        onClick={() => setPaletteOpen(true)}
      >
        <Search size={14} strokeWidth={2.2} aria-hidden="true" />
        <span>Search &amp; jump</span>
        <kbd>⌘K</kbd>
      </button>
      </div>

      {tab === "board" ? (
        <CommandCenter
          tasksByProject={fleet.tasksByProject}
          leasesByProject={fleet.leasesByProject}
          hosts={fleet.hosts}
          projects={fleet.projects}
          recentEvents={fleet.recentEvents}
          controls={controls}
          onOpenSession={setSelectedTask}
          onNewWork={() => setTab("intake")}
          loading={fleet.loading}
          selectedProjectId={selectedProjectId}
          onShowAllProjects={() => setSelectedProjectId(null)}
        />
      ) : tab === "fleet" ? (
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
          onViewTasks={() => setTab("board")}
        />
      )}
        </>
      )}

      <ConfirmDialog
        pending={controls.pending}
        onConfirm={controls.confirm}
        onCancel={controls.cancelConfirm}
      />

      {paletteOpen && (
        <CommandPalette
          tasksByProject={fleet.tasksByProject}
          onAction={runPaletteAction}
          onClose={() => setPaletteOpen(false)}
        />
      )}
    </div>
  );
}
