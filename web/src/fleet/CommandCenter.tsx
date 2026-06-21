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
import { useEffect, useMemo, useState } from "react";
import type { Event, Host, Lease, Task } from "../api/types.ts";
import { buildBoard, BOARD_COLUMNS } from "./board.ts";
import type { BoardCard } from "./board.ts";
import { isAwaitingApproval } from "./controls.ts";
import type { FleetControls } from "./useFleetControls.ts";
import "./board.css";

// selKey identifies a selected task across projects (project:id).
function selKey(projectId: string, taskId: string): string {
  return `${projectId}:${taskId}`;
}

export interface CommandCenterProps {
  tasksByProject: Record<string, Task[]>;
  leasesByProject: Record<string, Lease[]>;
  hosts: Host[];
  recentEvents: readonly Event[];
  // controls is the action layer; when omitted the board is read-only (no Approve).
  controls?: FleetControls;
  // onOpenSession opens a task's session detail view (the drill-in: card click / Review).
  onOpenSession?: (task: Task) => void;
  // onNewWork opens the intake flow ("+ New work").
  onNewWork?: () => void;
}

export function CommandCenter({
  tasksByProject,
  leasesByProject,
  hosts,
  recentEvents,
  controls,
  onOpenSession,
  onNewWork,
}: CommandCenterProps) {
  const board = buildBoard(tasksByProject, leasesByProject, recentEvents);

  // Multi-select bulk approve (redesign E4): a director can select several gate-green
  // (awaiting-approval) tasks and clear the review queue in one confirm. Only held
  // tasks are selectable — they are the only approvable ones. The selection is pruned
  // to the still-held set so approved tasks (which leave the held state after refresh)
  // drop out automatically.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const heldKeys = useMemo(() => {
    const s = new Set<string>();
    for (const tasks of Object.values(tasksByProject)) {
      for (const t of tasks) {
        if (isAwaitingApproval(t)) {
          s.add(selKey(t.project_id, t.id));
        }
      }
    }
    return s;
  }, [tasksByProject]);
  useEffect(() => {
    setSelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      for (const k of prev) {
        if (heldKeys.has(k)) next.add(k);
        else changed = true;
      }
      return changed ? next : prev;
    });
  }, [heldKeys]);

  const toggleSelect = (projectId: string, taskId: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      const k = selKey(projectId, taskId);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      return next;
    });
  };

  // selectedItems resolves the live selection to {projectId, taskId} for the bulk
  // approve, in a stable order (matches the held tasks' board order).
  const selectedItems = useMemo(() => {
    const items: { projectId: string; taskId: string }[] = [];
    for (const tasks of Object.values(tasksByProject)) {
      for (const t of tasks) {
        if (isAwaitingApproval(t) && selected.has(selKey(t.project_id, t.id))) {
          items.push({ projectId: t.project_id, taskId: t.id });
        }
      }
    }
    items.sort((a, b) =>
      a.projectId === b.projectId
        ? a.taskId.localeCompare(b.taskId)
        : a.projectId.localeCompare(b.projectId),
    );
    return items;
  }, [tasksByProject, selected]);

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

      {controls && selectedItems.length > 0 && (
        <div className="cc-bulkbar" role="region" aria-label="Bulk actions">
          <span className="cc-bulkbar-count">
            {selectedItems.length} selected
          </span>
          <span className="cc-bulkbar-actions">
            <button
              type="button"
              className="cc-btn cc-btn-approve"
              onClick={() => controls.requestBulkApprove(selectedItems)}
            >
              Approve &amp; merge {selectedItems.length}
            </button>
            <button
              type="button"
              className="cc-btn"
              onClick={() => setSelected(new Set())}
            >
              Clear
            </button>
          </span>
        </div>
      )}

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
                      {...(onOpenSession ? { onOpenSession } : {})}
                      selected={selected.has(selKey(card.task.project_id, card.task.id))}
                      onToggleSelect={() => toggleSelect(card.task.project_id, card.task.id)}
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
  onOpenSession?: (task: Task) => void;
  // selected/onToggleSelect drive the multi-select checkbox (held cards only).
  selected?: boolean;
  onToggleSelect?: () => void;
}

function BoardCardView({
  card,
  controls,
  onOpenSession,
  selected = false,
  onToggleSelect,
}: BoardCardViewProps) {
  const t = card.task;
  const held = isAwaitingApproval(t);
  const blocked = t.status === "blocked";
  const busy = controls?.isTaskBusy(t.project_id, t.id) ?? false;
  const select = () => onOpenSession?.(t);
  // Only held (approvable) cards can be multi-selected for a bulk approve.
  const selectable = held && controls !== undefined && onToggleSelect !== undefined;

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
  const cls = `cc-card cc-card-${kind}${selected ? " selected" : ""}`;

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
        {selectable && (
          // The checkbox is interactive within the card button; stop propagation so
          // ticking it doesn't also open the session.
          <label
            className="cc-card-check"
            onClick={(e) => e.stopPropagation()}
          >
            <input
              type="checkbox"
              checked={selected}
              onChange={onToggleSelect}
              aria-label={`Select task ${t.id} for bulk approve`}
            />
          </label>
        )}
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
          {onOpenSession && (
            <button type="button" className="cc-btn" onClick={select}>
              Review ▸
            </button>
          )}
        </div>
      )}
    </article>
  );
}
