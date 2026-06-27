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
import type { Event, Host, Lease, Project, Task } from "../api/types.ts";
import { buildBoard, BOARD_COLUMNS } from "./board.ts";
import type { BoardCard } from "./board.ts";
import { isAwaitingApproval } from "./controls.ts";
import type { FleetControls } from "./useFleetControls.ts";
import { Plus, ChevronRight, CircleDashed, Pause, Play } from "lucide-react";
import { Badge, Button, Card, Chip, Skeleton, StatusDot, Tooltip } from "../ui/index.ts";
import type { CardAccent } from "../ui/index.ts";
import "./board.css";

// SkeletonCard — the board's initial-load placeholder (before the first /status +
// task fetch lands). Shown only while loading AND the board is still empty.
function SkeletonCard() {
  return (
    <Card className="cc-card" aria-hidden="true">
      <Skeleton width="58%" />
      <Skeleton width="38%" />
    </Card>
  );
}

// selKey identifies a selected task across projects (project:id).
function selKey(projectId: string, taskId: string): string {
  return `${projectId}:${taskId}`;
}

export interface CommandCenterProps {
  tasksByProject: Record<string, Task[]>;
  leasesByProject: Record<string, Lease[]>;
  hosts: Host[];
  // projects carries each project's paused flag so a scoped board can offer Pause/Resume for
  // it. Optional: when omitted (or the scoped project is absent) the pause control is hidden.
  projects?: Project[];
  recentEvents: readonly Event[];
  // controls is the action layer; when omitted the board is read-only (no Approve).
  controls?: FleetControls;
  // onOpenSession opens a task's session detail view (the drill-in: card click / Review).
  onOpenSession?: (task: Task) => void;
  // onNewWork opens the intake flow ("+ New work").
  onNewWork?: () => void;
  // loading drives the initial-load skeleton (before the first data lands).
  loading?: boolean;
  // selectedProjectId (Faz-Q / Q1), when set, SCOPES the board to that one project — the
  // selection-driven view: picking a Conductor in the native sessions tree pushes a project
  // selection and the board narrows to it. Null/undefined → the cross-project fleet overview.
  selectedProjectId?: string | null;
  // onShowAllProjects clears the project scope back to the full fleet (the "Show all" affordance
  // shown while scoped). Omitted → no clear control (the scope is owned externally).
  onShowAllProjects?: () => void;
}

export function CommandCenter({
  tasksByProject,
  leasesByProject,
  hosts,
  projects,
  recentEvents,
  controls,
  onOpenSession,
  onNewWork,
  loading = false,
  selectedProjectId,
  onShowAllProjects,
}: CommandCenterProps) {
  // Faz-Q / Q1: when a project is selected (native tree → host selection), scope the board to it;
  // otherwise show the whole fleet. Scoping the SOURCE maps narrows the columns, the strip counts,
  // AND the bulk-approve set (they all derive from these) in one place. Memoized so the downstream
  // heldKeys/selectedItems memos keep a stable input across renders.
  const scopedTasks = useMemo(
    () =>
      selectedProjectId
        ? { [selectedProjectId]: tasksByProject[selectedProjectId] ?? [] }
        : tasksByProject,
    [selectedProjectId, tasksByProject],
  );
  const scopedLeases = useMemo(
    () =>
      selectedProjectId
        ? { [selectedProjectId]: leasesByProject[selectedProjectId] ?? [] }
        : leasesByProject,
    [selectedProjectId, leasesByProject],
  );
  const board = buildBoard(scopedTasks, scopedLeases, recentEvents);
  // Show placeholders only on the very first load (still fetching, nothing yet).
  const boardEmpty = BOARD_COLUMNS.every((c) => board.columns[c.key].length === 0);
  const showSkeleton = loading && boardEmpty;

  // Multi-select bulk actions (redesign E4 + quick-wins): a director can select several HELD
  // (gate-green) tasks to clear the review queue in one confirm (bulk approve), OR several BLOCKED
  // tasks to re-run them in one confirm (bulk retry). Only held/blocked tasks are selectable — the
  // two states that carry a bulk action. The selection is pruned to the still-actionable set so a
  // task that leaves held/blocked after refresh (approved or re-queued) drops out automatically.
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const selectableKeys = useMemo(() => {
    const s = new Set<string>();
    for (const tasks of Object.values(scopedTasks)) {
      for (const t of tasks) {
        if (isAwaitingApproval(t) || t.status === "blocked") {
          s.add(selKey(t.project_id, t.id));
        }
      }
    }
    return s;
  }, [scopedTasks]);
  useEffect(() => {
    setSelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      for (const k of prev) {
        if (selectableKeys.has(k)) next.add(k);
        else changed = true;
      }
      return changed ? next : prev;
    });
  }, [selectableKeys]);

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
    for (const tasks of Object.values(scopedTasks)) {
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
  }, [scopedTasks, selected]);

  // selectedBlocked resolves the live selection to the BLOCKED {projectId, taskId} for bulk retry,
  // in the same stable order. Held → bulk approve; blocked → bulk retry; the two never overlap.
  const selectedBlocked = useMemo(() => {
    const items: { projectId: string; taskId: string }[] = [];
    for (const tasks of Object.values(scopedTasks)) {
      for (const t of tasks) {
        if (t.status === "blocked" && selected.has(selKey(t.project_id, t.id))) {
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
  }, [scopedTasks, selected]);

  // The scoped project (when the board is narrowed to one) drives the Pause/Resume control in
  // the scope chip — so the director can halt or resume the whole pipeline from the board.
  const scopedProject =
    selectedProjectId != null && projects
      ? projects.find((p) => p.id === selectedProjectId)
      : undefined;

  return (
    <section className="cc" aria-label="Command Center">
      <div className="cc-strip">
        {selectedProjectId && (
          <span className="cc-scope" role="status">
            <Chip className="cc-scope-pill">
              <CircleDashed size={12} strokeWidth={2.2} aria-hidden="true" /> {selectedProjectId}
            </Chip>
            {controls && scopedProject && (
              controls.isPaused(scopedProject.id, scopedProject.paused) ? (
                <Button
                  variant="secondary"
                  size="sm"
                  leftIcon={<Play size={13} strokeWidth={2.2} />}
                  disabled={controls.isProjectBusy(scopedProject.id)}
                  onClick={() => controls.resume(scopedProject.id)}
                >
                  {controls.projectAction(scopedProject.id) === "resume" ? "Resuming…" : "Resume"}
                </Button>
              ) : (
                <Button
                  variant="secondary"
                  size="sm"
                  leftIcon={<Pause size={13} strokeWidth={2.2} />}
                  disabled={controls.isProjectBusy(scopedProject.id)}
                  onClick={() => controls.pause(scopedProject.id)}
                >
                  {controls.projectAction(scopedProject.id) === "pause" ? "Pausing…" : "Pause"}
                </Button>
              )
            )}
            {onShowAllProjects && (
              <button type="button" className="cc-scope-clear" onClick={onShowAllProjects}>
                Show all
              </button>
            )}
          </span>
        )}
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
            <Tooltip
              key={h.id}
              label={h.capabilities.length > 0 ? h.capabilities.join(" · ") : "no capabilities"}
            >
              <Chip className="cc-host-pill">
                <StatusDot tone="success" /> {h.id}
              </Chip>
            </Tooltip>
          ))}
          {onNewWork && (
            <Button
              variant="primary"
              size="sm"
              leftIcon={<Plus size={14} strokeWidth={2.2} />}
              onClick={onNewWork}
            >
              New work
            </Button>
          )}
        </span>
      </div>

      {controls && (selectedItems.length > 0 || selectedBlocked.length > 0) && (
        <div className="cc-bulkbar" role="region" aria-label="Bulk actions">
          <span className="cc-bulkbar-count">
            {selectedItems.length + selectedBlocked.length} selected
          </span>
          <span className="cc-bulkbar-actions">
            {selectedItems.length > 0 && (
              <Button
                variant="success"
                size="sm"
                onClick={() => controls.requestBulkApprove(selectedItems)}
              >
                Approve &amp; merge {selectedItems.length}
              </Button>
            )}
            {selectedBlocked.length > 0 && (
              <Button
                variant="primary"
                size="sm"
                onClick={() => controls.requestBulkRetry(selectedBlocked)}
              >
                Retry {selectedBlocked.length}
              </Button>
            )}
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
                {showSkeleton ? (
                  <>
                    <SkeletonCard />
                    <SkeletonCard />
                  </>
                ) : cards.length === 0 ? (
                  <div className="cc-col-empty">
                    <CircleDashed size={18} strokeWidth={1.5} aria-hidden="true" />
                    <span>No tasks</span>
                  </div>
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
  // selected/onToggleSelect drive the multi-select checkbox (held → bulk approve, blocked → retry).
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
  const rejected = t.status === "rejected";
  const running = t.status === "running";
  const busy = controls?.isTaskBusy(t.project_id, t.id) ?? false;
  // Abort is project-scoped (it cancels the in-flight task), so its busy/label key off
  // the project, not the task.
  const projBusy = controls?.isProjectBusy(t.project_id) ?? false;
  const select = () => onOpenSession?.(t);
  // Held (approvable) AND blocked (retryable) cards can be multi-selected — held → bulk approve,
  // blocked → bulk retry. Other states carry no bulk action, so they are not selectable.
  const selectable = (held || blocked) && controls !== undefined && onToggleSelect !== undefined;

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
              aria-label={`Select task ${t.id} for bulk ${held ? "approve" : "retry"}`}
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
        {card.liveActivity ? (
          <span className="cc-activity" title={card.liveActivity}>
            <StatusDot tone="info" pulse />
            <span className="cc-activity-text">{card.liveActivity}</span>
          </span>
        ) : card.livePhase ? (
          <span className="cc-phase">{card.livePhase}</span>
        ) : null}
      </div>
      {(blocked || rejected) && t.reason && (
        // WHY this card stalled or was declined — the agent's report summary (e.g. "…malformed
        // verdict…", "gate unresolved — needs user") or the director's reject reason ("rejected by
        // director"). Truncated; full text on hover via title.
        <div className="cc-card-reason" title={t.reason}>
          {t.reason}
        </div>
      )}
      {card.diff && (
        <div className="cc-card-diff">
          <span className="cc-add">+{card.diff.additions}</span>{" "}
          <span className="cc-del">−{card.diff.deletions}</span>{" "}
          <span className="muted">
            · {card.diff.files} file{card.diff.files === 1 ? "" : "s"}
          </span>
        </div>
      )}
      {(held || blocked || running || onOpenSession) && (
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
          {held && controls && (
            // Decline the held work: the counterpart to Approve so a held card is never a
            // dead-end (the user's "held'i sadece onaylayabiliyordum"). Confirm-gated.
            <Button
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => controls.requestReject(t.project_id, t.id)}
            >
              Reject
            </Button>
          )}
          {blocked && controls && (
            <Button
              variant="primary"
              size="sm"
              disabled={busy}
              onClick={() => controls.retry(t.project_id, t.id)}
            >
              {busy ? "Retrying…" : "Retry"}
            </Button>
          )}
          {running && controls && (
            <Button
              variant="danger"
              size="sm"
              disabled={projBusy}
              onClick={() => controls.requestAbort(t.project_id)}
            >
              {projBusy ? "Stopping…" : "Stop"}
            </Button>
          )}
          {onOpenSession && (
            <Button
              variant="secondary"
              size="sm"
              rightIcon={<ChevronRight size={13} strokeWidth={2.2} />}
              onClick={select}
            >
              Review
            </Button>
          )}
        </div>
      )}
    </Card>
  );
}
