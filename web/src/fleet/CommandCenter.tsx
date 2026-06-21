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
import { Badge, Button, Card, Chip, StatusDot } from "../ui/index.ts";
import type { CardAccent } from "../ui/index.ts";
import "./board.css";

// PlusIcon — the only board glyph kept inline (the primary CTA); the rest of the
// emoji/text glyphs are retired in V2. Real icon set (lucide) lands in V4.
const PlusIcon = () => (
  <svg width="13" height="13" viewBox="0 0 16 16" fill="none" aria-hidden="true">
    <path d="M8 3v10M3 8h10" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" />
  </svg>
);

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
            <Chip key={h.id} className="cc-host-pill" title={h.capabilities.join(", ")}>
              <StatusDot tone="success" /> {h.id}
            </Chip>
          ))}
          {onNewWork && (
            <Button variant="primary" size="sm" leftIcon={<PlusIcon />} onClick={onNewWork}>
              New work
            </Button>
          )}
        </span>
      </div>

      {controls && selectedItems.length > 0 && (
        <div className="cc-bulkbar" role="region" aria-label="Bulk actions">
          <span className="cc-bulkbar-count">
            {selectedItems.length} selected
          </span>
          <span className="cc-bulkbar-actions">
            <Button
              variant="success"
              size="sm"
              onClick={() => controls.requestBulkApprove(selectedItems)}
            >
              Approve &amp; merge {selectedItems.length}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>
              Clear
            </Button>
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

  // The status accent rail follows the card's lifecycle: held → amber (warn),
  // blocked → red (danger), running (a leasing host) → blue (info), done → green
  // (success), else a neutral ready.
  const accent: CardAccent = held
    ? "warn"
    : blocked
      ? "danger"
      : card.host
        ? "info"
        : t.status === "done"
          ? "success"
          : "none";

  return (
    <Card
      className="cc-card"
      interactive
      selected={selected}
      accent={accent}
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
        <span className="cc-card-id">{t.id}</span>
        {t.tier && <Badge tone="neutral">{t.tier}</Badge>}
      </div>
      <div className="cc-card-proj">{t.project_id}</div>
      <div className="cc-card-meta">
        {t.lane && <Chip>{t.lane}</Chip>}
        {card.host && (
          <span className="cc-host-tag">
            <StatusDot tone="success" pulse /> {card.host}
          </span>
        )}
        {card.livePhase && <span className="cc-phase">{card.livePhase}</span>}
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
            <Button
              variant="success"
              size="sm"
              disabled={busy}
              onClick={() => controls.requestApprove(t.project_id, t.id)}
            >
              {busy ? "Approving…" : "Approve"}
            </Button>
          )}
          {onOpenSession && (
            <Button variant="secondary" size="sm" onClick={select}>
              Review
            </Button>
          )}
        </div>
      )}
    </Card>
  );
}
