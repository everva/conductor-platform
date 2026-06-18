// Small, pure formatting helpers for the fleet dashboard. Kept separate from the
// components so they are unit-testable in isolation and reusable across panels.

// STALE_THRESHOLD_SECONDS: a host whose heartbeat is older than this is rendered
// with the "stale" warning style (ADR-0025 hosts surface heartbeat_age_seconds).
export const STALE_THRESHOLD_SECONDS = 60;

// humanAge renders a non-negative seconds count as a compact relative age
// ("12s ago", "3m ago", "2h ago", "1d ago"). It is used for heartbeat freshness.
export function humanAge(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  if (s < 60) {
    return `${s}s ago`;
  }
  const m = Math.floor(s / 60);
  if (m < 60) {
    return `${m}m ago`;
  }
  const h = Math.floor(m / 60);
  if (h < 24) {
    return `${h}h ago`;
  }
  const d = Math.floor(h / 24);
  return `${d}d ago`;
}

// HeartbeatFreshness describes how to render a host's heartbeat: the label plus a
// status the UI maps to a color. "never" = no/zero heartbeat ever recorded.
export interface HeartbeatFreshness {
  label: string;
  status: "fresh" | "stale" | "never";
}

// heartbeatFreshness classifies a host's heartbeat. A missing last_heartbeat, or a
// non-positive age with no timestamp, reads as "never"; otherwise fresh/stale by
// the staleness threshold.
export function heartbeatFreshness(
  ageSeconds: number,
  lastHeartbeat: string | undefined,
): HeartbeatFreshness {
  if (lastHeartbeat === undefined || lastHeartbeat.length === 0) {
    return { label: "never", status: "never" };
  }
  if (ageSeconds > STALE_THRESHOLD_SECONDS) {
    return { label: humanAge(ageSeconds), status: "stale" };
  }
  return { label: humanAge(ageSeconds), status: "fresh" };
}

// shortTime renders an ISO timestamp (or epoch ms) as a local HH:MM:SS, falling
// back to "—" when absent/invalid. Used for last-event / lease times.
export function shortTime(value: string | number | null | undefined): string {
  if (value === null || value === undefined || value === "") {
    return "—";
  }
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) {
    return "—";
  }
  return d.toLocaleTimeString();
}

// absoluteTime renders an ISO timestamp as a full local date-time string for the
// row title (hover) tooltip, falling back to the raw value when unparseable.
export function absoluteTime(value: string | null | undefined): string {
  if (value === null || value === undefined || value === "") {
    return "—";
  }
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) {
    return value;
  }
  return d.toLocaleString();
}

// payloadPreview compactly stringifies an event payload for inline display: empty
// payloads render as "" (caller omits the cell), and anything longer than `max`
// chars is truncated with an ellipsis so a large diff/log can't blow out the row.
export function payloadPreview(
  payload: Record<string, unknown>,
  max = 160,
): string {
  if (payload === null || typeof payload !== "object") {
    return "";
  }
  const keys = Object.keys(payload);
  if (keys.length === 0) {
    return "";
  }
  let s: string;
  try {
    s = JSON.stringify(payload);
  } catch {
    return "";
  }
  return s.length > max ? `${s.slice(0, max)}…` : s;
}
