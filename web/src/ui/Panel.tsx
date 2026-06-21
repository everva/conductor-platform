// Panel — a bordered surface with an optional header (title + actions) and body.
// Replaces .fleet-panel / .cc-col chrome. The header renders only when a `title`
// (or `actions`) is supplied, so a bare <Panel> is just a clean surface.
import type { HTMLAttributes, ReactNode } from "react";
import { cx } from "./cx.ts";

export interface PanelProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  /** Heading node (not the DOM title attribute — omitted from the base type). */
  title?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}

export function Panel({ title, actions, className, children, ...rest }: PanelProps) {
  const hasHead = title != null || actions != null;
  return (
    <div className={cx("ui-panel", className)} {...rest}>
      {hasHead && (
        <div className="ui-panel-head">
          {title != null && <h2 className="ui-panel-title">{title}</h2>}
          {actions != null && <div className="ui-panel-actions">{actions}</div>}
        </div>
      )}
      <div className="ui-panel-body">{children}</div>
    </div>
  );
}
