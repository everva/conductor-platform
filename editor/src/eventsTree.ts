// Conductor Platform — native "Conductor Events" TreeView (Faz-Q / Q2, ADR-0045).
//
// The live event feed, promoted from a buried cockpit TAB to a DEDICATED native surface in the
// PANEL area (alongside Output/Problems/Terminal) — the user's "events as its own window" ask,
// done native. A flat vscode.TreeDataProvider over the EventsWatcher's bounded, newest-first
// ring: each row carries a kind codicon + "phase/kind" label + "project·task · time" detail.
//
// TOKEN-FREE by construction: the watcher hands it only FeedEvents (id/ts/project/task/phase/
// kind); this module never sees the token and renders only those fields.
import * as vscode from "vscode";
import type { EventsWatcher, FeedEvent } from "./eventsWatcher";
import { describeEvent, type ActivityLevel } from "./activity";

/** View id of the native events tree (contributed in package.json, panel container). */
export const EVENTS_VIEW_ID = "conductor.events";

/** Command (registered in extension.ts) that opens an event's full detailed JSON in a read-only
 * virtual document — the "istersem detaylı json lara bakarım" drill-down. */
export const SHOW_EVENT_JSON_COMMAND = "conductor.showEventJson";

/** Severity → ThemeColor id for the stream row (drives the at-a-glance color of each line). */
function levelColor(level: ActivityLevel): string | undefined {
  switch (level) {
    case "attention":
      return "charts.yellow";
    case "done":
      return "charts.green";
    case "active":
      return "charts.blue";
    default:
      return undefined; // idle → default foreground (log/health noise)
  }
}

/** The actionable class of an event → its contextValue, so package.json `view/item/context` shows
 * the right inline actions (approve/retry/abort/open-diff). Pure; exported for tests. */
export function eventContextValue(ev: FeedEvent): string {
  if (ev.kind === "intervention-needed") {
    return "conductorEvent.review"; // approve / abort / open-diff
  }
  if (ev.kind === "decision" && (ev.result === "blocked" || ev.result === "changes-requested")) {
    return "conductorEvent.blocked"; // retry / open-diff
  }
  if (ev.kind === "diff") {
    return "conductorEvent.diff"; // open-diff
  }
  return "conductorEvent";
}

/**
 * Pure event-kind → presentation map (codicon id + optional ThemeColor id). GROUNDED in the
 * frozen event-kind vocabulary (web events.gen.ts): started / progress / log / diff / decision /
 * health / pr / merge / intervention-needed. Exported pure so a test pins each mapping without a
 * vscode runtime. Token-free (only the kind string).
 */
export function eventKindPresentation(kind: string): { readonly icon: string; readonly color?: string } {
  switch (kind) {
    case "intervention-needed":
      return { icon: "bell", color: "charts.yellow" };
    case "decision":
      return { icon: "shield", color: "charts.blue" };
    case "diff":
      return { icon: "git-compare", color: "charts.green" };
    case "merge":
      return { icon: "git-merge", color: "charts.purple" };
    case "pr":
      return { icon: "git-pull-request" };
    case "started":
      return { icon: "play-circle" };
    case "health":
      return { icon: "pulse" };
    case "progress":
      return { icon: "loading" };
    case "log":
      return { icon: "output" };
    default:
      return { icon: "circle-small-filled" };
  }
}

/** ISO → HH:MM:SS (best-effort; falls back to the raw ts). Pure; exported for tests. */
export function eventShortTime(ts: string): string {
  const m = /T(\d{2}:\d{2}:\d{2})/.exec(ts);
  return m ? m[1]! : ts;
}

/**
 * The native events tree. Flat (root → recent events, newest-first); leaves have no children.
 * `refresh()` fires onDidChangeTreeData so VS Code refetches — the watcher calls it on every
 * ring change (backfill + each live frame).
 */
export class EventsTreeProvider implements vscode.TreeDataProvider<FeedEvent> {
  readonly #watcher: EventsWatcher;
  readonly #emitter = new vscode.EventEmitter<FeedEvent | undefined>();
  readonly onDidChangeTreeData = this.#emitter.event;

  constructor(watcher: EventsWatcher) {
    this.#watcher = watcher;
  }

  /** Re-render the whole tree from the watcher's current ring (cheap; the ring is bounded). */
  refresh(): void {
    this.#emitter.fire(undefined);
  }

  getChildren(node?: FeedEvent): FeedEvent[] {
    // Flat list: only the root yields rows; events are leaves.
    return node === undefined ? [...this.#watcher.events()] : [];
  }

  getTreeItem(ev: FeedEvent): vscode.TreeItem {
    // The STREAM line: a plain-language description (not "phase / kind" jargon) prefixed with the
    // time, so the panel reads like a readable activity log. The full JSON is one click away.
    const { text, level } = describeEvent(ev);
    const time = eventShortTime(ev.ts);
    const item = new vscode.TreeItem(
      time ? `${time}  ${text}` : text,
      vscode.TreeItemCollapsibleState.None,
    );
    // Recognizable kind icon, colored by severity (yellow=needs-you, green=done, blue=active).
    const icon = eventKindPresentation(ev.kind).icon;
    const color = levelColor(level);
    item.iconPath = color ? new vscode.ThemeIcon(icon, new vscode.ThemeColor(color)) : new vscode.ThemeIcon(icon);
    // The locator (which task) rides in the dimmed description.
    item.description = ev.task ? `${ev.project}·${ev.task}` : ev.project;
    item.tooltip = `${text}\n${ev.phase ? `${ev.phase} / ` : ""}${ev.kind} — ${item.description}${time ? ` @ ${time}` : ""}\n(click for full JSON)`;
    item.contextValue = eventContextValue(ev);
    // Click → open the full detailed JSON (drill-down). The command reads the retained raw event.
    item.command = { command: SHOW_EVENT_JSON_COMMAND, title: "Show event JSON", arguments: [ev] };
    return item;
  }

  /** Dispose the change emitter (pushed to subscriptions). */
  dispose(): void {
    this.#emitter.dispose();
  }
}
