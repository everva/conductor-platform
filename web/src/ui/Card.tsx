// Card — a raised surface container. Optional interactive (hover-lift + pointer,
// rendered as a real button for keyboard access when `onClick` is given),
// selected ring, padding, and a left status accent rail. Replaces .cc-card.
import {
  forwardRef,
  type HTMLAttributes,
  type ReactNode,
} from "react";
import { cx } from "./cx.ts";

export type CardAccent = "info" | "warn" | "danger" | "success" | "none";

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  interactive?: boolean;
  selected?: boolean;
  /** Adds inner padding (omit when the card hosts its own header/body). */
  padded?: boolean;
  /** Left status rail color; "none" shows the faint default. */
  accent?: CardAccent;
  children: ReactNode;
}

export const Card = forwardRef<HTMLDivElement, CardProps>(function Card(
  { interactive = false, selected = false, padded = true, accent, className, children, ...rest },
  ref,
) {
  return (
    <div
      ref={ref}
      className={cx(
        "ui-card",
        padded && "ui-card-pad",
        interactive && "ui-card-interactive",
        selected && "ui-card-selected",
        accent && accent !== "none" && "ui-card-accent",
        accent && accent !== "none" && `ui-card-accent-${accent}`,
        className,
      )}
      {...rest}
    >
      {children}
    </div>
  );
});
