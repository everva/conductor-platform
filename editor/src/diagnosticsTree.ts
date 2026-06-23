// Conductor Platform — native "Diagnostics" TreeView (Status panel).
//
// A vscode.TreeDataProvider that renders the {@link DiagRow}s from diagnostics.ts as a flat
// native list in the Conductor activity-bar container — the at-a-glance debug surface for the
// editor↔gateway↔credential wiring. It is fed by an injected async `gather()` (so it stays
// testable with a fake and the network/secret probes live in extension.ts), and `refresh()`
// (a title button + command) re-gathers. TOKEN-FREE: it only ever renders DiagRow values, which
// the diagnostics model guarantees are token-free.
import * as vscode from "vscode";
import type { DiagRow, DiagLevel } from "./diagnostics";

/** View id of the native diagnostics tree (contributed in package.json, Conductor container). */
export const DIAGNOSTICS_VIEW_ID = "conductor.diagnostics";

/** Maps a row level → a codicon id + ThemeColor id. Pure so a test pins each mapping. */
export function diagLevelIcon(level: DiagLevel): { readonly icon: string; readonly color?: string } {
  switch (level) {
    case "ok":
      return { icon: "pass-filled", color: "charts.green" };
    case "warn":
      return { icon: "warning", color: "charts.yellow" };
    case "bad":
      return { icon: "error", color: "charts.red" };
    case "info":
      return { icon: "info" };
  }
}

/**
 * The native Diagnostics tree. Flat (root → one item per row). `gather` is injected — it returns
 * the freshly-probed rows (extension.ts builds it from the connection state + reachability +
 * secret-presence + the token-free credential probe). `refresh()` fires onDidChangeTreeData so
 * the tree re-calls `gather`. Holds no token and no client — purely a renderer.
 */
export class DiagnosticsTreeProvider implements vscode.TreeDataProvider<DiagRow> {
  readonly #gather: () => Promise<DiagRow[]>;
  readonly #changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.#changed.event;

  constructor(gather: () => Promise<DiagRow[]>) {
    this.#gather = gather;
  }

  /** Re-probe + repaint (manual refresh command / on connection-state change). */
  refresh(): void {
    this.#changed.fire();
  }

  getTreeItem(row: DiagRow): vscode.TreeItem {
    const item = new vscode.TreeItem(row.label, vscode.TreeItemCollapsibleState.None);
    // The value is the description (right-aligned, muted) — a label + value read like a status row.
    item.description = row.value;
    item.tooltip = `${row.label}: ${row.value}`;
    const { icon, color } = diagLevelIcon(row.level);
    item.iconPath = color ? new vscode.ThemeIcon(icon, new vscode.ThemeColor(color)) : new vscode.ThemeIcon(icon);
    return item;
  }

  /** Root → the probed rows; rows have no children (flat). */
  async getChildren(element?: DiagRow): Promise<DiagRow[]> {
    if (element !== undefined) {
      return [];
    }
    return this.#gather();
  }

  dispose(): void {
    this.#changed.dispose();
  }
}
