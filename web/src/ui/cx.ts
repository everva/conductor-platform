// Tiny className joiner for the UI primitives — drops falsy parts so callers can
// write conditional classes inline: cx("ui-btn", active && "is-active").
export function cx(
  ...parts: Array<string | false | null | undefined>
): string {
  return parts.filter(Boolean).join(" ");
}
