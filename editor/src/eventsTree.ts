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

/** View id of the native events tree (contributed in package.json, panel container). */
export const EVENTS_VIEW_ID = "conductor.events";

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
    const item = new vscode.TreeItem(
      ev.phase ? `${ev.phase} / ${ev.kind}` : ev.kind,
      vscode.TreeItemCollapsibleState.None,
    );
    const pres = eventKindPresentation(ev.kind);
    item.iconPath = pres.color
      ? new vscode.ThemeIcon(pres.icon, new vscode.ThemeColor(pres.color))
      : new vscode.ThemeIcon(pres.icon);
    const loc = ev.task ? `${ev.project}·${ev.task}` : ev.project;
    const time = eventShortTime(ev.ts);
    item.description = time ? `${loc} · ${time}` : loc;
    item.tooltip = `${ev.phase ? `${ev.phase} / ` : ""}${ev.kind} — ${loc}${time ? ` @ ${time}` : ""}`;
    item.contextValue = "conductorEvent";
    return item;
  }

  /** Dispose the change emitter (pushed to subscriptions). */
  dispose(): void {
    this.#emitter.dispose();
  }
}
