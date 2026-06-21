// palette.ts — the PURE model behind the ⌘K command palette (agent-native
// redesign, E4 "director powers"). A director drives the whole command center
// from the keyboard: jump to any cockpit surface, start new work, or open any
// task's session — without reaching for the mouse.
//
// Like board.ts/session.ts it is deliberately pure (no React, no I/O): the same
// tasksByProject snapshot useFleet already holds goes in with a query string, an
// ordered, filtered list of palette items comes out. That keeps the matching
// unit-testable and the component a thin keyboard-driven view. No gateway surface
// is added — the palette is composed entirely from the fleet snapshot (B3).
import type { Task } from "../api/types.ts";

// PaletteSurface is a navigable cockpit tab (mirrors FleetDashboard's DashboardTab).
export type PaletteSurface = "board" | "fleet" | "events" | "intake";

// PaletteAction is what executing an item does. The component maps each to a
// FleetDashboard callback (navigate a surface / start new work / open a session).
export type PaletteAction =
  | { type: "navigate"; surface: PaletteSurface }
  | { type: "new-work" }
  | { type: "open-session"; task: Task };

// PaletteGroup orders items into labelled sections the palette renders top→down.
export type PaletteGroup = "Go to" | "Actions" | "Sessions";

// PaletteItem is one selectable row. `keywords` is the lowercased haystack the
// query matches against; `hint` is the right-aligned context (project · status).
export interface PaletteItem {
  id: string;
  group: PaletteGroup;
  label: string;
  hint: string | null;
  keywords: string;
  action: PaletteAction;
}

// GROUP_ORDER fixes the section order so the list (and keyboard nav) is stable.
const GROUP_ORDER: readonly PaletteGroup[] = ["Go to", "Actions", "Sessions"];

// staticItems are the always-present navigation + action rows (independent of the
// fleet). "New work" is the verb for the intake surface, so intake is reached via
// the action rather than a duplicate "Go to Intake" row.
function staticItems(): PaletteItem[] {
  return [
    {
      id: "go:board",
      group: "Go to",
      label: "Go to Board",
      hint: null,
      keywords: "go to board command center kanban",
      action: { type: "navigate", surface: "board" },
    },
    {
      id: "go:fleet",
      group: "Go to",
      label: "Go to Fleet",
      hint: null,
      keywords: "go to fleet projects hosts",
      action: { type: "navigate", surface: "fleet" },
    },
    {
      id: "go:events",
      group: "Go to",
      label: "Go to Events",
      hint: null,
      keywords: "go to events feed stream log",
      action: { type: "navigate", surface: "events" },
    },
    {
      id: "act:new-work",
      group: "Actions",
      label: "+ New work",
      hint: "spec → dispatch",
      keywords: "new work intake spec dispatch create task",
      action: { type: "new-work" },
    },
  ];
}

// sessionItems turns every task across every project into a "jump to session" row.
// A task has no human title (titles live on its scenario), so it is labelled by id
// with project · status context; lane/tier/status widen the search haystack.
function sessionItems(tasksByProject: Record<string, Task[]>): PaletteItem[] {
  const out: PaletteItem[] = [];
  for (const tasks of Object.values(tasksByProject)) {
    for (const t of tasks) {
      out.push({
        id: `session:${t.project_id}:${t.id}`,
        group: "Sessions",
        label: t.id,
        hint: `${t.project_id} · ${t.status}`,
        keywords: `${t.id} ${t.project_id} ${t.lane} ${t.tier} ${t.status}`.toLowerCase(),
        action: { type: "open-session", task: t },
      });
    }
  }
  // Stable order: by item id (project then task id), matching the board's ordering.
  out.sort((a, b) => a.id.localeCompare(b.id));
  return out;
}

// matches returns true when EVERY whitespace-separated token of the (trimmed,
// lowercased) query appears in the item's label+keywords — an AND match, so
// "web auth" narrows to items mentioning both. An empty query matches everything.
function matches(item: PaletteItem, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (q === "") {
    return true;
  }
  const hay = `${item.label} ${item.keywords}`.toLowerCase();
  return q.split(/\s+/).every((tok) => hay.includes(tok));
}

// buildPaletteItems composes the ordered, filtered item list for a query. Items
// keep their group order (Go to → Actions → Sessions); within a group their
// natural order is preserved (JS sort is stable). The component renders these and
// drives selection by a single flat index, so the order here IS the nav order.
export function buildPaletteItems(
  tasksByProject: Record<string, Task[]>,
  query: string,
): PaletteItem[] {
  const all = [...staticItems(), ...sessionItems(tasksByProject)];
  return all
    .filter((it) => matches(it, query))
    .sort((a, b) => GROUP_ORDER.indexOf(a.group) - GROUP_ORDER.indexOf(b.group));
}
