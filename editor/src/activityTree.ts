// Conductor Platform — native "Activity" TreeView (the digestible "Now" panel).
//
// A vscode.TreeDataProvider that renders the {@link ActivityRow}s (one plain-language line per
// active task) in the Conductor activity-bar container. It is fed by an injected `rows()` getter
// (extension.ts wires it to summarizeActivity(watcher.events())) and `refresh()` is fired from
// the events watcher's onChange, so it updates live as the agent reports progress. TOKEN-FREE: it
// only renders ActivityRow text, which the model guarantees is token-free.
import * as vscode from "vscode";
import type { ActivityRow, ActivityLevel } from "./activity";

/** View id of the native Activity tree (contributed in package.json, Conductor container). */
export const ACTIVITY_VIEW_ID = "conductor.activity";

/** Maps an activity level → a codicon id + ThemeColor id. Pure so a test pins each mapping. */
export function activityLevelIcon(level: ActivityLevel): { readonly icon: string; readonly color?: string } {
  switch (level) {
    case "active":
      return { icon: "loading~spin", color: "charts.blue" };
    case "attention":
      return { icon: "bell", color: "charts.yellow" };
    case "done":
      return { icon: "pass-filled", color: "charts.green" };
    case "idle":
      return { icon: "circle-outline" };
  }
}

/**
 * The native Activity tree. Flat (root → one row per active task). `rows` is injected (returns
 * the freshly-summarized activity); `refresh()` fires onDidChangeTreeData so the tree re-reads
 * `rows`. Holds no token, no client — purely a renderer.
 */
export class ActivityTreeProvider implements vscode.TreeDataProvider<ActivityRow> {
  readonly #rows: () => ActivityRow[];
  readonly #changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.#changed.event;

  constructor(rows: () => ActivityRow[]) {
    this.#rows = rows;
  }

  /** Repaint (on every events-watcher change). */
  refresh(): void {
    this.#changed.fire();
  }

  getTreeItem(row: ActivityRow): vscode.TreeItem {
    // Label = task id; description = the plain-language "what's happening now".
    const item = new vscode.TreeItem(row.task, vscode.TreeItemCollapsibleState.None);
    item.description = row.text;
    item.tooltip = `${row.project}/${row.task}: ${row.text}`;
    const { icon, color } = activityLevelIcon(row.level);
    item.iconPath = color ? new vscode.ThemeIcon(icon, new vscode.ThemeColor(color)) : new vscode.ThemeIcon(icon);
    // Clicking a row opens that task's session (deep-link), reusing the existing command.
    item.command = {
      command: "conductor.openSession",
      title: "Open Session",
      arguments: [row.project, row.task],
    };
    return item;
  }

  /** Root → the activity rows; rows are leaves (flat). */
  getChildren(element?: ActivityRow): ActivityRow[] {
    if (element !== undefined) {
      return [];
    }
    return this.#rows();
  }

  dispose(): void {
    this.#changed.dispose();
  }
}
