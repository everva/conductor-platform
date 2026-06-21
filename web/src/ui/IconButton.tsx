// IconButton — square, icon-only action. An accessible label is REQUIRED (icon
// buttons have no text), enforced via the `label` prop → aria-label.
import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { cx } from "./cx.ts";

export interface IconButtonProps
  extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "aria-label"> {
  /** Required accessible name (icon-only buttons have no visible text). */
  label: string;
  size?: "sm" | "md";
  /** Adds a subtle surface + border (vs the default bare ghost look). */
  bordered?: boolean;
  children: ReactNode;
}

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(
  function IconButton(
    { label, size = "md", bordered = false, className, children, type = "button", ...rest },
    ref,
  ) {
    return (
      <button
        ref={ref}
        type={type}
        aria-label={label}
        title={label}
        className={cx(
          "ui-iconbtn",
          `ui-iconbtn-${size}`,
          bordered && "ui-iconbtn-bordered",
          className,
        )}
        {...rest}
      >
        {children}
      </button>
    );
  },
);
