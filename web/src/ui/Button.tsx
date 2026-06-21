// Button — the primary action primitive. Token-driven variants + sizes, a
// built-in loading spinner, and optional icon slots. Forwards its ref so it can
// anchor menus/tooltips later. Replaces the hand-rolled .cc-btn / .fleet-btn.
import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { cx } from "./cx.ts";

export type ButtonVariant =
  | "primary"
  | "secondary"
  | "ghost"
  | "success"
  | "danger";
export type ButtonSize = "sm" | "md";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  /** Shows a spinner and disables interaction. */
  loading?: boolean;
  leftIcon?: ReactNode;
  rightIcon?: ReactNode;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      variant = "secondary",
      size = "md",
      loading = false,
      leftIcon,
      rightIcon,
      disabled,
      className,
      children,
      type = "button",
      ...rest
    },
    ref,
  ) {
    return (
      <button
        ref={ref}
        type={type}
        className={cx(
          "ui-btn",
          `ui-btn-${size}`,
          `ui-btn-${variant}`,
          className,
        )}
        disabled={disabled || loading}
        aria-busy={loading || undefined}
        {...rest}
      >
        {loading ? (
          <span className="ui-btn-spinner" aria-hidden="true" />
        ) : (
          leftIcon
        )}
        {children}
        {!loading && rightIcon}
      </button>
    );
  },
);
