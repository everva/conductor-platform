// Tooltip — a lightweight, dependency-free hover/focus tooltip. Wraps a trigger
// and reveals a token-styled label above (or below) it. Accessible: the label is a
// role="tooltip" element wired via aria-describedby, and it shows on keyboard focus
// (focus bubbles from a focusable child), not just hover. Decorative for non-
// focusable children (hover-only) — pass interactive children for full a11y.
import { useId, useState, type ReactNode } from "react";
import { cx } from "./cx.ts";

export interface TooltipProps {
  label: string;
  side?: "top" | "bottom";
  children: ReactNode;
}

export function Tooltip({ label, side = "top", children }: TooltipProps) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <span
      className="ui-tooltip-wrap"
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      onFocus={() => setOpen(true)}
      onBlur={() => setOpen(false)}
    >
      <span aria-describedby={open ? id : undefined} style={{ display: "inline-flex" }}>
        {children}
      </span>
      {open && (
        <span role="tooltip" id={id} className={cx("ui-tooltip", `ui-tooltip-${side}`)}>
          {label}
        </span>
      )}
    </span>
  );
}
