// Conductor Platform — diagnostics model (native Status/Diagnostics panel).
//
// Pure (vscode-free) so it is unit-tested directly: takes a token-FREE snapshot of the
// editor↔gateway↔credential state and turns it into labelled rows the native TreeView renders.
// This is the at-a-glance debug surface — "why isn't the login reaching the gateway?" — built
// from facts the host already knows (connection state, reachability, secret PRESENCE booleans)
// plus a token-free credential presence probe.
//
// TOKEN DISCIPLINE: the snapshot carries only the gateway URL, enum states, and BOOLEAN presence
// flags — never a token. `bearerStored`/`claudeLocal` are booleans (not the values), and
// `claudeOnGateway` is a status enum from a probe that reads only the HTTP code. So no row can
// ever render a secret.

import type { ConnectionState } from "./connection";
import type { CredentialStatus } from "./credentialClient";

/** Severity of a diagnostic row → drives the codicon/color in the tree. */
export type DiagLevel = "ok" | "warn" | "bad" | "info";

/** One rendered diagnostic row: a label, a token-free value string, and a severity. */
export interface DiagRow {
  readonly label: string;
  readonly value: string;
  readonly level: DiagLevel;
}

/** A token-FREE snapshot of the current editor↔gateway↔credential state. */
export interface DiagSnapshot {
  readonly gatewayUrl: string;
  readonly connection: ConnectionState;
  /** /healthz probe outcome. */
  readonly healthz: "ok" | "fail" | "unknown";
  /** /readyz probe outcome. */
  readonly readyz: "ok" | "fail" | "unknown";
  /** Whether a gateway bearer token is stored (presence only). */
  readonly bearerStored: boolean;
  /** Whether a claude OAuth token is stored in the editor (presence only). */
  readonly claudeLocal: boolean;
  /** Whether the claude credential is present on the gateway (token-free probe). */
  readonly claudeOnGateway: CredentialStatus;
}

/** Connection state → row value + level. */
function connectionRow(s: ConnectionState): DiagRow {
  const level: DiagLevel = s === "connected" ? "ok" : s === "error" ? "bad" : "warn";
  return { label: "Connection", value: s, level };
}

/** A reachability probe outcome → row. `unknown` (not yet probed / no URL) is warn, not bad. */
function probeRow(label: string, outcome: "ok" | "fail" | "unknown"): DiagRow {
  const level: DiagLevel = outcome === "ok" ? "ok" : outcome === "fail" ? "bad" : "warn";
  return { label, value: outcome, level };
}

/** The claude-on-gateway credential status → a row with an actionable value string. */
function claudeGatewayRow(s: CredentialStatus): DiagRow {
  switch (s) {
    case "present":
      return { label: "Claude login (gateway)", value: "present", level: "ok" };
    case "absent":
      return { label: "Claude login (gateway)", value: "absent — run “Push Login to Gateway”", level: "warn" };
    case "not-configured":
      return { label: "Claude login (gateway)", value: "gateway has no encryption key (503)", level: "bad" };
    case "unauthorized":
      return { label: "Claude login (gateway)", value: "unauthorized (bad bearer)", level: "bad" };
    case "not-connected":
      return { label: "Claude login (gateway)", value: "no bearer — connect first", level: "warn" };
    case "unreachable":
      return { label: "Claude login (gateway)", value: "gateway unreachable", level: "bad" };
  }
}

/**
 * Builds the diagnostic rows from a snapshot. PURE + deterministic → unit-tested. The order is
 * top-down causal: where you point → are you connected → is the gateway up → do you hold the
 * secrets → did the login reach the gateway. Every value is token-free.
 */
export function buildDiagnosticRows(snap: DiagSnapshot): DiagRow[] {
  return [
    { label: "Gateway URL", value: snap.gatewayUrl || "(unset)", level: snap.gatewayUrl ? "info" : "bad" },
    connectionRow(snap.connection),
    probeRow("Gateway /healthz", snap.healthz),
    probeRow("Gateway /readyz", snap.readyz),
    {
      label: "Bearer token (editor)",
      value: snap.bearerStored ? "stored" : "missing — run “Connect to Gateway”",
      level: snap.bearerStored ? "ok" : "warn",
    },
    {
      label: "Claude login (editor)",
      value: snap.claudeLocal ? "stored" : "missing — run “Log In”",
      level: snap.claudeLocal ? "ok" : "warn",
    },
    claudeGatewayRow(snap.claudeOnGateway),
  ];
}
