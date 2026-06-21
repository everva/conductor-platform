// StatusDot — a small colored status indicator. `pulse` adds a live halo (for
// running work / an open connection). Decorative by default; pass a `label` to
// expose it to assistive tech, otherwise it is aria-hidden.
import { cx } from "./cx.ts";

export type DotTone = "neutral" | "success" | "warn" | "danger" | "info" | "brand";

export interface StatusDotProps {
  tone?: DotTone;
  pulse?: boolean;
  label?: string;
  className?: string;
}

export function StatusDot({ tone = "neutral", pulse = false, label, className }: StatusDotProps) {
  return (
    <span
      className={cx(
        "ui-dot",
        tone !== "neutral" && `ui-dot-${tone}`,
        pulse && "ui-dot-pulse",
        className,
      )}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    />
  );
}
