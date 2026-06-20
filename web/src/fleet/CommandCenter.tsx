// CommandCenter.tsx — the agent-native Command Center board (redesign E1): the
// DEFAULT cockpit surface a director watches, a status-Kanban of every task across
// every project/host (decision: docs/EDITOR-AGENT-NATIVE-REDESIGN-PLAN.md §11,
// Devin-Desktop / Windsurf model). Four lifecycle columns — Ready, Running, Needs
// Review, Done — with the director's action lane (Needs Review = awaiting-approval +
// blocked) surfaced in the middle and a top strip of the counts that matter first.
//
// It is a THIN view over the pure board model (buildBoard) and reuses the existing
// data layer (the useFleet snapshot) and action layer (useFleetControls) verbatim —
// no new gateway surface (B3). A card click focuses its project (the E1 drill-in;
// E2 replaces this with a full session view). Awaiting-approval cards get the same
// confirm-gated Approve the TasksView uses; blocked cards offer Review.
import type { Event, Host, Lease, Task } from "../api/types.ts";
import { buildBoard, BOARD_COLUMNS } from "./board.ts";
import type { BoardCard } from "./board.ts";
import { isAwaitingApproval } from "./controls.ts";
import type { FleetControls } from "./useFleetControls.ts";
import "./board.css";

export interface CommandCenterProps {
  tasksByProject: Record<string, Task[]>;
  leasesByProject: Record<string, Lease[]>;
  hosts: Host[];
  recentEvents: readonly Event[];
  // controls is the action layer; when omitted the board is read-only (no Approve).
  controls?: FleetControls;
  // onSelectProject focuses a project (the E1 drill-in; a card click / Review).
  onSelectProject?: (projectId: string) => void;
  // onNewWork opens the intake flow ("+ New work").
  onNewWork?: () => void;
}

export function CommandCenter({
  tasksByProject,
  leasesByProject,
  hosts,
  recentEvents,
  controls,
  onSelectProject,
  onNewWork,
}: CommandCenterProps) {
  const board = buildBoard(tasksByProject, leasesByProject, recentEvents);

  return (
    <section className="cc" aria-label="Command Center">
      <div className="cc-strip">
        <span className="cc-stat">
          <b>{board.counts.running}</b> running
        </span>
        <span className={board.counts.needsReview > 0 ? "cc-stat cc-attn" : "cc-stat"}>
          <b>{board.counts.needsReview}</b> need your review
        </span>
        <span className="cc-stat">
          <b>{board.counts.blocked}</b> blocked
        </span>
        <span className="cc-strip-right">
          {hosts.map((h) => (
            <span key={h.id} className="cc-host-pill" title={h.capabilities.join(", ")}>
              <span className="cc-dot" /> {h.id}
            </span>
          ))}
          {onNewWork && (
            <button type="button" className="cc-newwork" onClick={onNewWork}>
              + New work
            </button>
          )}
        </span>
      </div>

      <div className="cc-board" role="list" aria-label="Task board">
        {BOARD_COLUMNS.map((col) => {
          const cards = board.columns[col.key];
          return (
            <div
              key={col.key}
              className={`cc-col cc-col-${col.key}`}
              role="listitem"
              aria-label={`${col.label} (${cards.length})`}
            >
              <div className="cc-col-head">
                <span>{col.label}</span>
                <span className="cc-col-count">{cards.length}</span>
              </div>
              <div className="cc-col-body">
                {cards.length === 0 ? (
                  <p className="cc-col-empty">—</p>
                ) : (
                  cards.map((card) => (
                    <BoardCardView
                      key={card.task.id}
                      card={card}
                      {...(controls ? { controls } : {})}
                      {...(onSelectProject ? { onSelectProject } : {})}
                    />
                  ))
                )}
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}

interface BoardCardViewProps {
  card: BoardCard;
  controls?: FleetControls;
  onSelectProject?: (projectId: string) => void;
}

function BoardCardView({ card, controls, onSelectProject }: BoardCardViewProps) {
  const t = card.task;
  const held = isAwaitingApproval(t);
  const blocked = t.status === "blocked";
  const busy = controls?.isTaskBusy(t.project_id, t.id) ?? false;
  const select = () => onSelectProject?.(t.project_id);

  // The status accent rail follows the card's lifecycle: held → amber, blocked →
  // red, running (a leasing host) → blue, done → green, else a neutral ready.
  const kind = held
    ? "attn"
    : blocked
      ? "blocked"
      : card.host
        ? "running"
        : t.status === "done"
          ? "done"
          : "ready";
  const cls = `cc-card cc-card-${kind}`;

  return (
    <article
      className={cls}
      role="button"
      tabIndex={0}
      aria-label={`Task ${t.id} in ${t.project_id}, status ${held ? "awaiting-approval" : t.status}`}
      onClick={select}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          select();
        }
      }}
    >
      <div className="cc-card-top">
        <span className="mono cc-card-id">{t.id}</span>
        {t.tier && <span className="cc-card-tier">{t.tier}</span>}
      </div>
      <div className="cc-card-proj">{t.project_id}</div>
      <div className="cc-card-meta">
        {t.lane && <span className="cc-card-lane">{t.lane}</span>}
        {card.host && (
          <span className="cc-host-tag">
            <span className="cc-dot cc-dot-live" /> {card.host}
          </span>
        )}
        {card.livePhase && <span className="cc-phase">▸ {card.livePhase}</span>}
      </div>
      {card.diff && (
        <div className="cc-card-diff">
          <span className="cc-add">+{card.diff.additions}</span>{" "}
          <span className="cc-del">−{card.diff.deletions}</span>{" "}
          <span className="muted">
            · {card.diff.files} file{card.diff.files === 1 ? "" : "s"}
          </span>
        </div>
      )}
      {(held || blocked) && (
        <div className="cc-card-actions" onClick={(e) => e.stopPropagation()}>
          {held && controls && (
            <button
              type="button"
              className="cc-btn cc-btn-approve"
              disabled={busy}
              onClick={() => controls.requestApprove(t.project_id, t.id)}
            >
              {busy ? "Approving…" : "Approve"}
            </button>
          )}
          {onSelectProject && (
            <button type="button" className="cc-btn" onClick={select}>
              Review ▸
            </button>
          )}
        </div>
      )}
    </article>
  );
}
