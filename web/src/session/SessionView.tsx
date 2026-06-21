// SessionView — the agent-native session detail surface (redesign E2): drill into
// ONE task and see, Devin-style, the whole run a director needs to judge it:
//   * SPEC — the scenario's acceptance criteria the agent is held to (B2 read);
//   * VERIFIER VERDICT — the deterministic per-gate decision (B1/ADR-0033), the
//     platform's trust differentiator (machine-proven, not an LLM's "looks good");
//   * DIFF — the bounded branch-vs-base change (4C-1 KindDiff), rendered inline;
//   * ACTIVITY — the develop→verify→verdict→diff→merge/hold timeline;
//   * CONTROLS — Approve a held task (confirm-gated, same path as the board).
//
// Data: the task's event stream via useEventFeed({project, task}) (history backfill
// + live WS, transport-agnostic) drives verdict/diff/timeline; the scenario is
// fetched once via listScenarios. Both injection seams (web token / fork transport)
// mirror FleetDashboard so this mounts in either host.
import { useEffect, useMemo, useState } from "react";
import { ApiClient } from "../api/client.ts";
import type { Event, Scenario, Task } from "../api/types.ts";
import type { EventTransport } from "../api/useEventStream.ts";
import { useEventFeed } from "../events/useEventFeed.ts";
import type { HistoryLoader } from "../events/useEventFeed.ts";
import { isAwaitingApproval } from "../fleet/controls.ts";
import type { FleetControls } from "../fleet/useFleetControls.ts";
import { Check, ChevronLeft, X } from "lucide-react";
import { Badge, Button, Chip, StatusDot } from "../ui/index.ts";
import type { BadgeTone } from "../ui/index.ts";
import { buildTimeline, parseDiff, parseVerdict } from "./session.ts";
import "./session.css";

// Map a task's lifecycle status to a Badge tone for the header chip.
function statusTone(status: string, held: boolean): BadgeTone {
  if (held) return "warn";
  if (status === "blocked") return "danger";
  if (status === "done") return "success";
  if (status === "running") return "info";
  return "neutral";
}

// ScenarioClient is the single read the session needs for the spec.
export interface ScenarioClient {
  listScenarios(projectId: string): Promise<Scenario[]>;
}

export interface SessionViewProps {
  task: Task;
  token: string;
  onBack: () => void;
  onUnauthorized: () => void;
  controls?: FleetControls;
  // Injection (web token vs fork transport), mirroring FleetDashboard.
  makeScenarioClient?: (token: string) => ScenarioClient;
  makeHistory?: (token: string) => HistoryLoader;
  eventTransport?: EventTransport;
}

function shortTime(ts: string): string {
  // ISO → HH:MM:SS (best-effort; falls back to the raw ts).
  const m = /T(\d{2}:\d{2}:\d{2})/.exec(ts);
  return m ? m[1]! : ts;
}

// diffLineClass colors a unified-diff line for the inline diff render.
function diffLineClass(line: string): string {
  if (line.startsWith("@@")) return "sv-diff-hunk";
  if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff ") || line.startsWith("index ")) {
    return "sv-diff-meta";
  }
  if (line.startsWith("+")) return "sv-diff-add";
  if (line.startsWith("-")) return "sv-diff-del";
  return "";
}

export function SessionView({
  task,
  token,
  onBack,
  onUnauthorized,
  controls,
  makeScenarioClient,
  makeHistory,
  eventTransport,
}: SessionViewProps) {
  const enabled = eventTransport ? true : token.length > 0;

  const feed = useEventFeed({
    token,
    filter: { project: task.project_id, task: task.id },
    ...(makeHistory ? { makeHistory } : {}),
    ...(eventTransport ? { eventTransport, enabled: true } : {}),
  });

  // The scenario carries the SPEC (acceptance criteria). Fetched once per task.
  const [scenario, setScenario] = useState<Scenario | null>(null);
  useEffect(() => {
    if (!enabled || !task.scenario_id) {
      return;
    }
    const client: ScenarioClient = makeScenarioClient
      ? makeScenarioClient(token)
      : new ApiClient({ token });
    let cancelled = false;
    client
      .listScenarios(task.project_id)
      .then((scs) => {
        if (!cancelled) {
          setScenario(scs.find((s) => s.id === task.scenario_id) ?? null);
        }
      })
      .catch(() => {
        if (!cancelled) {
          setScenario(null);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [task.project_id, task.scenario_id, token, enabled, makeScenarioClient]);

  // Bubble a 401 from the feed to the app (sign-out), like FleetDashboard.
  useEffect(() => {
    if (feed.error?.unauthorized) {
      onUnauthorized();
    }
  }, [feed.error, onUnauthorized]);

  const events: readonly Event[] = feed.events;
  // Replay (E4): selecting a timeline entry pins the verdict + diff panels to the
  // state AS OF that moment; null = follow live (the latest, today's behavior).
  const [replayTs, setReplayTs] = useState<string | null>(null);
  const verdict = useMemo(() => parseVerdict(events, replayTs ?? undefined), [events, replayTs]);
  const diff = useMemo(() => parseDiff(events, replayTs ?? undefined), [events, replayTs]);
  const timeline = useMemo(() => buildTimeline(events), [events]);

  const held = isAwaitingApproval(task);
  const busy = controls?.isTaskBusy(task.project_id, task.id) ?? false;
  const title = scenario?.title || task.id;

  return (
    <section className="sv" aria-label="Session">
      <div className="sv-head">
        <Button
          variant="ghost"
          size="sm"
          leftIcon={<ChevronLeft size={15} strokeWidth={2.2} />}
          onClick={onBack}
        >
          Command Center
        </Button>
        <div className="sv-head-main">
          <span className="sv-head-id">{task.project_id} / {task.id}</span>
          <h2 className="sv-head-title">{title}</h2>
        </div>
        <div className="sv-head-meta">
          {task.tier && <Badge tone="neutral">{task.tier}</Badge>}
          {task.lane && <Chip>{task.lane}</Chip>}
          <Badge tone={statusTone(task.status, held)}>
            {held ? "awaiting-approval" : task.status}
          </Badge>
        </div>
      </div>

      <div className="sv-grid">
        <div className="sv-col">
          {/* SPEC */}
          <section className="sv-panel" aria-label="Spec">
            <h3 className="sv-panel-head">Spec</h3>
            {scenario && scenario.acceptance.length > 0 ? (
              <ul className="sv-accept">
                {scenario.acceptance.map((a, i) => (
                  <li key={i}>{a}</li>
                ))}
              </ul>
            ) : (
              <p className="sv-muted">{scenario ? "No acceptance criteria recorded." : "Loading spec…"}</p>
            )}
            {scenario?.hidden_holdout_ref && (
              <p className="sv-holdout">
                <span className="sv-muted">hidden holdout:</span>{" "}
                <span className="mono">{scenario.hidden_holdout_ref}</span>
              </p>
            )}
          </section>

          {/* VERIFIER VERDICT — the trust differentiator */}
          <section className="sv-panel sv-verdict" aria-label="Verifier verdict">
            <h3 className="sv-panel-head">
              Verifier verdict <span className="sv-muted">· deterministic gate</span>
              {replayTs !== null && <span className="sv-replay-tag">replay</span>}
            </h3>
            {verdict ? (
              <>
                <ul className="sv-checks">
                  {verdict.checks.map((c, i) => (
                    <li key={i} className={c.result === "pass" ? "sv-check-pass" : "sv-check-fail"}>
                      <span className="sv-check-icon" aria-hidden="true">
                        {c.result === "pass" ? (
                          <Check size={14} strokeWidth={2.6} />
                        ) : (
                          <X size={14} strokeWidth={2.6} />
                        )}
                      </span>
                      <span className="sv-check-name">{c.name}</span>
                      <span className="sv-check-evidence mono">{c.evidence}</span>
                    </li>
                  ))}
                </ul>
                <p className={verdict.result === "pass" ? "sv-verdict-line sv-pass" : "sv-verdict-line sv-fail"}>
                  {verdict.result === "pass"
                    ? "→ MERGE-READY · machine-proven"
                    : "→ CHANGES REQUESTED · gate not satisfied"}
                </p>
              </>
            ) : (
              <p className="sv-muted">Awaiting the gate…</p>
            )}
          </section>
        </div>

        <div className="sv-col">
          {/* ACTIVITY — a replayable timeline: select an entry to pin the verdict
              + diff panels to that moment (E4). */}
          <section className="sv-panel" aria-label="Activity">
            <h3 className="sv-panel-head">
              Activity
              {replayTs !== null && (
                <button
                  type="button"
                  className="sv-replay-live"
                  onClick={() => setReplayTs(null)}
                >
                  <StatusDot tone="success" pulse /> Return to live
                </button>
              )}
            </h3>
            {replayTs !== null && (
              <p className="sv-replay-note" role="status">
                Replaying as of {shortTime(replayTs)} — the verdict and diff show the
                state at this point.
              </p>
            )}
            {timeline.length === 0 ? (
              <p className="sv-muted">No activity yet.</p>
            ) : (
              <ol className="sv-timeline">
                {timeline
                  .slice()
                  .reverse()
                  .map((e) => {
                    const selected = e.ts === replayTs;
                    return (
                      <li key={e.id} className={`sv-tl sv-tl-${e.kind}`}>
                        <button
                          type="button"
                          className={selected ? "sv-tl-btn selected" : "sv-tl-btn"}
                          aria-pressed={selected}
                          onClick={() => setReplayTs(selected ? null : e.ts)}
                        >
                          <span className="sv-tl-time mono">{shortTime(e.ts)}</span>
                          <span className="sv-tl-label">{e.label}</span>
                          {e.summary && (
                            <span className="sv-tl-summary mono">{e.summary}</span>
                          )}
                        </button>
                      </li>
                    );
                  })}
              </ol>
            )}
          </section>

          {/* DIFF */}
          <section className="sv-panel" aria-label="Diff">
            <h3 className="sv-panel-head">
              Diff{" "}
              {diff && (
                <span className="sv-muted">
                  · <span className="sv-add">+{diff.additions}</span>{" "}
                  <span className="sv-del">−{diff.deletions}</span> · {diff.files.length} file
                  {diff.files.length === 1 ? "" : "s"}
                  {diff.truncated ? " · truncated" : ""}
                </span>
              )}
              {replayTs !== null && <span className="sv-replay-tag">replay</span>}
            </h3>
            {diff ? (
              <>
                <ul className="sv-difffiles">
                  {diff.files.map((f, i) => (
                    <li key={i}>
                      <span className="sv-difffile-status">{f.status}</span>
                      <span className="mono">{f.path}</span>
                      <span className="sv-muted">
                        <span className="sv-add">+{f.additions}</span>{" "}
                        <span className="sv-del">−{f.deletions}</span>
                      </span>
                    </li>
                  ))}
                </ul>
                {diff.patch && (
                  <pre className="sv-patch">
                    {diff.patch.split("\n").map((line, i) => (
                      <div key={i} className={diffLineClass(line)}>
                        {line || " "}
                      </div>
                    ))}
                  </pre>
                )}
              </>
            ) : (
              <p className="sv-muted">No diff yet (emitted once the gate passes).</p>
            )}
          </section>
        </div>
      </div>

      {(held || task.status === "running") && controls && (
        <div className="sv-actions">
          {held && (
            <Button
              variant="success"
              disabled={busy}
              onClick={() => controls.requestApprove(task.project_id, task.id)}
            >
              {busy ? "Approving…" : "Approve & merge"}
            </Button>
          )}
          <Button
            variant="danger"
            onClick={() => controls.requestAbort(task.project_id)}
          >
            Abort
          </Button>
        </div>
      )}

      {feed.error && (
        <p role="alert" className="sv-error">
          {feed.error.message}
        </p>
      )}
    </section>
  );
}
