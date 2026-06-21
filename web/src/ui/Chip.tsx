// Chip — a neutral, low-emphasis metadata tag (lane, capability, host). For
// status semantics use <Badge>. `mono` renders identifiers in the mono face.
import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "./cx.ts";

export interface ChipProps extends HTMLAttributes<HTMLSpanElement> {
  mono?: boolean;
  children: ReactNode;
}

export function Chip({ mono = false, className, children, ...rest }: ChipProps) {
  return (
    <span className={cx("ui-chip", mono && "ui-chip-mono", className)} {...rest}>
      {children}
    </span>
  );
}
