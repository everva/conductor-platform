// Conductor Platform — native "Conductors" sessions TreeView (Faz-4 native-IDE layout, N2).
//
// The Devin "Your Devins" rail, done NATIVE: a vscode.TreeDataProvider that shows projects
// (Conductors) → their tasks (sessions), each task carrying a status codicon (the at-a-glance
// lifecycle read). It replaces the dar webview board as the sidebar's PRIMARY content — the
// board itself now lives in the editor area (N1 Command Center). Clicking a TASK deep-links the
// Command Center to that session (N3, conductor.openSession); clicking a project reveals it.
//
// DATA: fed by the host-side authed FleetReadClient (token in the header only — never reaches
// this provider or a TreeItem). getChildren fetches lazily (root → projects, project → tasks);
// refresh() refetches. TOKEN-FREE by construction: this module never sees the token.
import * as vscode from "vscode";
import type { FleetProject, FleetReadClient } from "./fleetReadClient";

/** View id of the native sessions tree (contributed in package.json, activity-bar container). */
export const SESSIONS_VIEW_ID = "conductor.sessions";

/** A node in the Conductors tree: a project (Conductor) or one of its tasks (a session). */
export type SessionNode =
  | { readonly kind: "project"; readonly project: FleetProject }
  | {
      readonly kind: "task";
      readonly projectId: string;
      readonly task: { readonly id: string; readonly lane: string; readonly tier: string; readonly status: string };
    };

/**
 * Pure status → presentation map (codicon id + optional ThemeColor id). GROUNDED in the board's
 * status vocabulary (web/src/fleet/board.ts columnFor): running / awaiting-approval / blocked /
 * done; everything else (ready / todo / unknown) → a neutral dot. Exported pure so a test pins
 * each mapping without a vscode runtime. Token-free (only the status string).
 */
export function taskStatusPresentation(status: string): { readonly icon: string; readonly color?: string } {
  switch (status) {
    case "running":
      return { icon: "play-circle", color: "charts.blue" };
    case "awaiting-approval":
      // The director's review lane — echoes the 4C-3 review motif.
      return { icon: "git-pull-request", color: "charts.yellow" };
    case "blocked":
      return { icon: "warning", color: "charts.red" };
    case "done":
      return { icon: "pass-filled", color: "charts.green" };
    default:
      return { icon: "circle-outline" };
  }
}

/**
 * The native "Conductors" tree. Lazy + async: root yields projects, a project yields its tasks.
 * The command ids are INJECTED (so this module needs no back-import from extension.ts):
 * `openCommand` (`conductor.open`) reveals the Command Center for a project click;
 * `openSessionCommand` (`conductor.openSession`) deep-links it to a task's session (N3).
 * `refresh()` (manual command + on connection-state change) fires onDidChangeTreeData to refetch.
 */
export class SessionsTreeProvider implements vscode.TreeDataProvider<SessionNode> {
  readonly #client: FleetReadClient;
  readonly #openCommand: string;
  readonly #openSessionCommand: string;
  readonly #emitter = new vscode.EventEmitter<SessionNode | undefined>();
  readonly onDidChangeTreeData = this.#emitter.event;

  constructor(client: FleetReadClient, openCommand: string, openSessionCommand: string) {
    this.#client = client;
    this.#openCommand = openCommand;
    this.#openSessionCommand = openSessionCommand;
  }

  /** Refetch the whole tree (the gateway reads are cheap + the tree is small). */
  refresh(): void {
    this.#emitter.fire(undefined);
  }

  async getChildren(node?: SessionNode): Promise<SessionNode[]> {
    if (node === undefined) {
      const projects = await this.#client.listProjects();
      return projects.map((project) => ({ kind: "project", project }) as const);
    }
    if (node.kind === "project") {
      const tasks = await this.#client.listTasks(node.project.id);
      return tasks.map(
        (task) =>
          ({
            kind: "task",
            projectId: node.project.id,
            task: { id: task.id, lane: task.lane, tier: task.tier, status: task.status },
          }) as const,
      );
    }
    // Tasks are leaves.
    return [];
  }

  getTreeItem(node: SessionNode): vscode.TreeItem {
    if (node.kind === "project") {
      const { project } = node;
      const item = new vscode.TreeItem(project.id, vscode.TreeItemCollapsibleState.Collapsed);
      item.iconPath = new vscode.ThemeIcon("repo");
      item.description = project.paused ? "paused" : project.readiness;
      item.contextValue = "conductorProject";
      item.tooltip = `Conductor ${project.id}${project.paused ? " (paused)" : ""}`;
      // Clicking a project reveals the Command Center too (the board shows all its sessions).
      item.command = { command: this.#openCommand, title: "Open Command Center" };
      return item;
    }
    const { task } = node;
    const item = new vscode.TreeItem(task.id, vscode.TreeItemCollapsibleState.None);
    const pres = taskStatusPresentation(task.status);
    item.iconPath = pres.color
      ? new vscode.ThemeIcon(pres.icon, new vscode.ThemeColor(pres.color))
      : new vscode.ThemeIcon(pres.icon);
    item.description = `${task.tier} · ${task.lane} · ${task.status}`;
    item.contextValue = "conductorTask";
    item.tooltip = `${task.id} — ${task.status} (${task.tier}/${task.lane})`;
    // Click → deep-link the Command Center to THIS session (N3): the args reach the
    // conductor.openSession handler, which navigates the cockpit's webview to its SessionView.
    item.command = {
      command: this.#openSessionCommand,
      title: "Open Session",
      arguments: [node.projectId, task.id],
    };
    return item;
  }

  /** Dispose the change emitter (pushed to subscriptions). */
  dispose(): void {
    this.#emitter.dispose();
  }
}
