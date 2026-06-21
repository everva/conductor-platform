// CommandPalette: the ⌘K director overlay (agent-native redesign, E4). One
// keyboard-driven surface to jump to any cockpit view, start new work, or open
// any task's session. Type to filter (AND over tokens), ↑/↓ to move the
// highlight, Enter to run it, Esc or a backdrop click to dismiss. The matching/
// ordering lives in the pure palette.ts model; this is a thin view over it.
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { buildPaletteItems } from "./palette.ts";
import type { PaletteAction, PaletteItem } from "./palette.ts";
import type { Task } from "../api/types.ts";
import "./palette.css";

export interface CommandPaletteProps {
  tasksByProject: Record<string, Task[]>;
  // onAction runs the selected item (navigate / new work / open session). The
  // parent closes the palette as part of executing it.
  onAction: (action: PaletteAction) => void;
  onClose: () => void;
}

export function CommandPalette({ tasksByProject, onAction, onClose }: CommandPaletteProps) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  const items = useMemo(
    () => buildPaletteItems(tasksByProject, query),
    [tasksByProject, query],
  );

  // Focus the input on open so a director can type immediately.
  useEffect(() => {
    inputRef.current?.focus();
  }, []);
  // Reset the highlight to the top whenever the result set changes.
  useEffect(() => {
    setActive(0);
  }, [query]);

  // The highlight is clamped to the current results so it never points past the end
  // after the list shrinks (no separate state to keep in sync).
  const clampedActive = items.length === 0 ? -1 : Math.min(active, items.length - 1);

  const run = (item: PaletteItem | undefined) => {
    if (item) {
      onAction(item.action);
    }
  };

  const onKeyDown = (e: ReactKeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => (items.length === 0 ? 0 : Math.min(i + 1, items.length - 1)));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      run(items[clampedActive]);
    } else if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    }
  };

  let lastGroup: string | null = null;

  return (
    // Backdrop click dismisses; clicks inside the panel are stopped so they don't.
    <div className="cmdk-backdrop" onMouseDown={onClose}>
      <div
        className="cmdk"
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="cmdk-input"
          type="text"
          placeholder="Jump to a session, switch view, or start new work…"
          aria-label="Command palette query"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onKeyDown}
        />
        {items.length === 0 ? (
          <p className="cmdk-empty">No matches.</p>
        ) : (
          <ul className="cmdk-list" role="listbox" aria-label="Commands">
            {items.map((item, i) => {
              const header = item.group !== lastGroup;
              lastGroup = item.group;
              const isActive = i === clampedActive;
              return (
                <Fragment key={item.id}>
                  {header && (
                    <li className="cmdk-group" role="presentation" aria-hidden="true">
                      {item.group}
                    </li>
                  )}
                  <li
                    role="option"
                    aria-selected={isActive}
                    className={isActive ? "cmdk-item active" : "cmdk-item"}
                    onMouseEnter={() => setActive(i)}
                    onMouseDown={(e) => {
                      // mousedown (not click) so the item runs before the input blurs.
                      e.preventDefault();
                      run(item);
                    }}
                  >
                    <span className="cmdk-item-label">{item.label}</span>
                    {item.hint && <span className="cmdk-item-hint">{item.hint}</span>}
                  </li>
                </Fragment>
              );
            })}
          </ul>
        )}
        <div className="cmdk-foot">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> navigate
          </span>
          <span>
            <kbd>↵</kbd> open
          </span>
          <span>
            <kbd>esc</kbd> close
          </span>
        </div>
      </div>
    </div>
  );
}
