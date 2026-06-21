// Badge — a small status pill (paused / approved / blocked / tier …). Tone maps
// to the semantic status tokens (soft bg + text + border). For neutral metadata
// tags (lane, capability) use <Chip> instead.
import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "./cx.ts";

export type BadgeTone =
  | "neutral"
  | "brand"
  | "success"
  | "warn"
  | "danger"
  | "info";

export interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: BadgeTone;
  children: ReactNode;
}

export function Badge({ tone = "neutral", className, children, ...rest }: BadgeProps) {
  return (
    <span className={cx("ui-badge", `ui-badge-${tone}`, className)} {...rest}>
      {children}
    </span>
  );
}
