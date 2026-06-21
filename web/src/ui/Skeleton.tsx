// Skeleton — a loading-shimmer placeholder. Defaults to a text line; `circle`
// renders a round avatar/dot placeholder. Width/height accept any CSS length.
import type { CSSProperties } from "react";
import { cx } from "./cx.ts";

export interface SkeletonProps {
  width?: string | number;
  height?: string | number;
  circle?: boolean;
  className?: string;
  /** Accessible label for the busy region (defaults to a generic one). */
  label?: string;
}

export function Skeleton({
  width,
  height,
  circle = false,
  className,
  label = "Loading",
}: SkeletonProps) {
  const style: CSSProperties = {
    width,
    height: circle ? (height ?? width) : height,
  };
  return (
    <span
      role="status"
      aria-label={label}
      aria-busy="true"
      className={cx(
        "ui-skeleton",
        circle ? "ui-skeleton-circle" : "ui-skeleton-line",
        className,
      )}
      style={style}
    />
  );
}
