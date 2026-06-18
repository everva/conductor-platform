// EventStreamView: the full live event-stream cockpit (3B-2). It backfills recent
// history then follows the live WS feed (both via useEventFeed, which reuses
// ApiClient.listEvents + useEventStream — no hand-rolled fetch/socket), rendering a
// scrollable feed NEWEST-FIRST at the top (the common ops choice: the freshest
// signal is where the eye lands, no auto-scroll-to-bottom fight). Each row shows the
// relative time (absolute on hover), project·task, a phase badge, a kind badge, and a
// compact payload preview. `intervention-needed` rows are highlighted distinctly with
// an explicit marker — the human-gate signal (ADR-0011) must be impossible to miss.
//
// Filters (project/task/phase/kind/intervention-only) drive BOTH the history backfill
// and the live subscription through one EventQuery so the two stay consistent; a
// filter change cleanly re-queries history and re-subscribes the socket. Stream
// controls: PAUSE freezes the display (UI-side, NOT the conductor pause) and CLEAR
// empties the view.
import { useMemo, useState } from "react";
import { KIND_INTERVENTION_NEEDED } from "../types/events.gen.ts";
import type { Kind, Phase } from "../api/types.ts";
import type { EventQuery } from "../api/types.ts";
import { useEventFeed } from "./useEventFeed.ts";
import type { HistoryLoader } from "./useEventFeed.ts";
import { absoluteTime, payloadPreview, shortTime } from "../fleet/format.ts";
import "../fleet/fleet.css";
import "./events.css";

// Phase / Kind option lists for the dropdowns. Mirrors the frozen vocabulary in
// events.gen.ts (the codegen exports only the type unions, so the literal lists live
// here, typed as the unions so a vocabulary change is a compile error).
const PHASE_OPTIONS: readonly Phase[] = [
  "plan",
  "develop",
  "test",
  "review",
  "verify",
  "merge",
];

const KIND_OPTIONS: readonly Kind[] = [
  "started",
  "progress",
  "log",
  "diff",
  "decision",
  "health",
  "pr",
  "merge",
  "intervention-needed",
];

export interface EventStreamViewProps {
  token: string;
  // projects optionally seeds the project filter dropdown (from the dashboard); when
  // omitted/empty the project filter falls back to a free-text input.
  projects?: string[];
  // onUnauthorized bubbles a 401 from the backfill so the app can sign out.
  onUnauthorized?: () => void;
  // makeHistory is injectable for tests (defaults to the real ApiClient in useEventFeed).
  makeHistory?: (token: string) => HistoryLoader;
  // historyLimit / bufferCap are exposed for tests; sensible defaults otherwise.
  historyLimit?: number;
  bufferCap?: number;
}

// FilterState is the local form state. Empty strings mean "no filter" and are pruned
// before building the EventQuery.
interface FilterState {
  project: string;
  task: string;
  phase: string;
  kind: string;
  interventionOnly: boolean;
}

const EMPTY_FILTER: FilterState = {
  project: "",
  task: "",
  phase: "",
  kind: "",
  interventionOnly: false,
};

// toQuery prunes empty fields so the gateway receives only the active filters (and
// so useEventFeed's filterKey is stable when nothing is set).
function toQuery(f: FilterState): EventQuery {
  const q: EventQuery = {};
  if (f.project) q.project = f.project;
  if (f.task) q.task = f.task;
  if (f.phase) q.phase = f.phase;
  if (f.kind) q.kind = f.kind;
  if (f.interventionOnly) q.intervention = true;
  return q;
}

export function EventStreamView({
  token,
  projects,
  onUnauthorized,
  makeHistory,
  historyLimit = 200,
  bufferCap = 1000,
}: EventStreamViewProps) {
  const [filter, setFilter] = useState<FilterState>(EMPTY_FILTER);
  const [paused, setPaused] = useState(false);

  // Memoize the query off the filter state so a fresh object literal each render does
  // not retrigger useEventFeed's backfill/subscription unnecessarily (the filter only
  // changes via setFilter, and useEventFeed re-keys off the serialized query anyway).
  const query = useMemo(() => toQuery(filter), [filter]);

  const feed = useEventFeed({
    token,
    filter: query,
    paused,
    historyLimit,
    bufferCap,
    ...(makeHistory ? { makeHistory } : {}),
  });

  // Bubble a 401 to the app (same contract as the fleet dashboard).
  if (feed.error?.unauthorized && onUnauthorized) {
    onUnauthorized();
  }

  // Render newest-first: the buffer is ascending by ts, so reverse for display.
  const rows = useMemo(() => [...feed.events].reverse(), [feed.events]);

  const set = <K extends keyof FilterState>(key: K, value: FilterState[K]) => {
    setFilter((prev) => ({ ...prev, [key]: value }));
  };

  const hasProjectList = Array.isArray(projects) && projects.length > 0;

  return (
    <section className="fleet-panel evt" aria-label="Event stream">
      <div className="fleet-panel-head evt-head">
        <h2>Event stream</h2>
        <span className="evt-controls">
          <span className="fleet-conn" aria-label={`Stream ${feed.state}`}>
            <span className={`fleet-dot ${feed.state}`} />
            {feed.state}
          </span>
          {feed.state === "closed" && (
            <button type="button" onClick={feed.reload} title="Reconnect / reload">
              Reconnect
            </button>
          )}
          <button
            type="button"
            aria-pressed={paused}
            className={paused ? "evt-toggle on" : "evt-toggle"}
            onClick={() => setPaused((p) => !p)}
          >
            {paused ? "Resume" : "Pause"}
          </button>
          <button type="button" onClick={feed.clear}>
            Clear
          </button>
          <span className="muted evt-count">{feed.events.length}</span>
        </span>
      </div>

      <form
        className="evt-filters"
        aria-label="Event filters"
        onSubmit={(e) => e.preventDefault()}
      >
        {hasProjectList ? (
          <select
            aria-label="Project filter"
            value={filter.project}
            onChange={(e) => set("project", e.target.value)}
          >
            <option value="">All projects</option>
            {projects!.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        ) : (
          <input
            type="text"
            aria-label="Project filter"
            placeholder="project"
            value={filter.project}
            onChange={(e) => set("project", e.target.value)}
          />
        )}
        <input
          type="text"
          aria-label="Task filter"
          placeholder="task"
          value={filter.task}
          onChange={(e) => set("task", e.target.value)}
        />
        <select
          aria-label="Phase filter"
          value={filter.phase}
          onChange={(e) => set("phase", e.target.value)}
        >
          <option value="">All phases</option>
          {PHASE_OPTIONS.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
        <select
          aria-label="Kind filter"
          value={filter.kind}
          onChange={(e) => set("kind", e.target.value)}
        >
          <option value="">All kinds</option>
          {KIND_OPTIONS.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
        <label className="evt-toggle-label">
          <input
            type="checkbox"
            checked={filter.interventionOnly}
            onChange={(e) => set("interventionOnly", e.target.checked)}
          />
          intervention only
        </label>
      </form>

      {feed.error !== null && !feed.error.unauthorized && (
        <p role="alert" className="fleet-error">
          {feed.error.message}{" "}
          <button type="button" onClick={feed.reload}>
            Retry
          </button>
        </p>
      )}

      <div className="fleet-panel-body evt-feed" role="log" aria-live="off">
        {rows.length === 0 ? (
          <p className="fleet-empty">
            {feed.loading ? "Loading events…" : "No events match the current filter."}
          </p>
        ) : (
          rows.map((e) => {
            const intervention = e.kind === KIND_INTERVENTION_NEEDED;
            const preview = payloadPreview(e.payload);
            return (
              <div
                key={e.id}
                className={`evt-row${intervention ? " intervention" : ""}`}
                data-testid="evt-row"
              >
                <span className="evt-time" title={absoluteTime(e.ts)}>
                  {shortTime(e.ts)}
                </span>
                <span className="evt-loc mono">
                  {e.project}
                  {e.task ? `·${e.task}` : ""}
                </span>
                <span className={`badge evt-phase ${e.phase}`}>{e.phase}</span>
                <span
                  className={`badge evt-kind${intervention ? " abort" : ""}`}
                >
                  {e.kind}
                </span>
                {intervention && (
                  <span className="evt-marker" aria-label="intervention needed">
                    ⚠ intervention
                  </span>
                )}
                {preview && <span className="evt-payload mono muted">{preview}</span>}
              </div>
            );
          })
        )}
      </div>
    </section>
  );
}
