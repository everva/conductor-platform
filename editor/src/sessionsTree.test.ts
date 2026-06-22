// Headless tests for the native "Conductors" sessions tree (N2). `vscode` is aliased to the
// mock (vitest.config), so TreeItem/ThemeIcon/ThemeColor/EventEmitter are the mock's stand-ins;
// a fake FleetReadClient drives getChildren without a network. No electron, no display.
import { describe, expect, it, vi } from "vitest";
import { TreeItemCollapsibleState } from "../test/vscode-mock";
import { SessionsTreeProvider, taskStatusPresentation, SESSIONS_VIEW_ID } from "./sessionsTree";
import type { FleetReadClient, FleetProject, FleetTask } from "./fleetReadClient";

const OPEN = "conductor.open";
const OPEN_SESSION = "conductor.openSession";

function fakeClient(
  over: {
    projects?: FleetProject[];
    tasksByProject?: Record<string, FleetTask[]>;
  } = {},
): FleetReadClient {
  return {
    listProjects: vi.fn(() => Promise.resolve(over.projects ?? [])),
    listTasks: vi.fn((id: string) => Promise.resolve(over.tasksByProject?.[id] ?? [])),
  } as unknown as FleetReadClient;
}

describe("taskStatusPresentation", () => {
  it("maps the board status vocabulary to a codicon + color; default is a neutral dot", () => {
    expect(taskStatusPresentation("running")).toEqual({ icon: "play-circle", color: "charts.blue" });
    expect(taskStatusPresentation("awaiting-approval")).toEqual({
      icon: "git-pull-request",
      color: "charts.yellow",
    });
    expect(taskStatusPresentation("blocked")).toEqual({ icon: "warning", color: "charts.red" });
    expect(taskStatusPresentation("done")).toEqual({ icon: "pass-filled", color: "charts.green" });
    expect(taskStatusPresentation("todo")).toEqual({ icon: "circle-outline" });
    expect(taskStatusPresentation("")).toEqual({ icon: "circle-outline" });
  });
});

describe("SessionsTreeProvider", () => {
  it("getChildren(root) lists projects as project nodes", async () => {
    const client = fakeClient({ projects: [{ id: "web-shop", readiness: "ready", paused: false }] });
    const nodes = await new SessionsTreeProvider(client, OPEN, OPEN_SESSION).getChildren();
    expect(nodes).toEqual([
      { kind: "project", project: { id: "web-shop", readiness: "ready", paused: false } },
    ]);
  });

  it("getChildren(project) lists that project's tasks as task nodes", async () => {
    const client = fakeClient({
      tasksByProject: {
        "web-shop": [
          { id: "W-1", projectId: "web-shop", lane: "web", tier: "T3", status: "awaiting-approval" },
        ],
      },
    });
    const provider = new SessionsTreeProvider(client, OPEN, OPEN_SESSION);
    const nodes = await provider.getChildren({
      kind: "project",
      project: { id: "web-shop", readiness: "ready", paused: false },
    });
    expect(nodes).toEqual([
      {
        kind: "task",
        projectId: "web-shop",
        task: { id: "W-1", lane: "web", tier: "T3", status: "awaiting-approval" },
      },
    ]);
    expect(client.listTasks).toHaveBeenCalledWith("web-shop");
  });

  it("task nodes are leaves (getChildren returns [])", async () => {
    const provider = new SessionsTreeProvider(fakeClient(), OPEN, OPEN_SESSION);
    const node = {
      kind: "task",
      projectId: "p",
      task: { id: "t", lane: "l", tier: "T1", status: "done" },
    } as const;
    expect(await provider.getChildren(node)).toEqual([]);
  });

  it("getTreeItem(project) → repo icon, collapsible, readiness description, reveal-CC command", () => {
    const provider = new SessionsTreeProvider(fakeClient(), OPEN, OPEN_SESSION);
    const item = provider.getTreeItem({
      kind: "project",
      project: { id: "web-shop", readiness: "ready", paused: false },
    });
    expect(item.label).toBe("web-shop");
    expect(item.collapsibleState).toBe(TreeItemCollapsibleState.Collapsed);
    expect((item.iconPath as { id: string }).id).toBe("repo");
    expect(item.description).toBe("ready");
    expect(item.contextValue).toBe("conductorProject");
    expect(item.command).toEqual({ command: OPEN, title: "Open Command Center" });
  });

  it("getTreeItem(paused project) shows 'paused' as the description", () => {
    const provider = new SessionsTreeProvider(fakeClient(), OPEN, OPEN_SESSION);
    const item = provider.getTreeItem({
      kind: "project",
      project: { id: "api", readiness: "ready", paused: true },
    });
    expect(item.description).toBe("paused");
  });

  it("getTreeItem(task) → status codicon+color, 'tier · lane · status' description, reveal-CC command", () => {
    const provider = new SessionsTreeProvider(fakeClient(), OPEN, OPEN_SESSION);
    const item = provider.getTreeItem({
      kind: "task",
      projectId: "web-shop",
      task: { id: "W-1", lane: "web", tier: "T3", status: "awaiting-approval" },
    });
    expect(item.label).toBe("W-1");
    expect(item.collapsibleState).toBe(TreeItemCollapsibleState.None);
    const icon = item.iconPath as { id: string; color?: { id: string } };
    expect(icon.id).toBe("git-pull-request");
    expect(icon.color?.id).toBe("charts.yellow");
    expect(item.description).toBe("T3 · web · awaiting-approval");
    expect(item.contextValue).toBe("conductorTask");
    // N3: a task click deep-links the Command Center to this session (openSession + args).
    expect(item.command).toEqual({
      command: OPEN_SESSION,
      title: "Open Session",
      arguments: ["web-shop", "W-1"],
    });
  });

  it("refresh() fires onDidChangeTreeData", () => {
    const provider = new SessionsTreeProvider(fakeClient(), OPEN, OPEN_SESSION);
    const listener = vi.fn();
    provider.onDidChangeTreeData(listener);
    provider.refresh();
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("exposes the contributed view id", () => {
    expect(SESSIONS_VIEW_ID).toBe("conductor.sessions");
  });
});
