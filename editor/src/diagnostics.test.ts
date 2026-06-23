// Deterministic tests for the pure diagnostics model. No vscode, no network.
import { describe, expect, it } from "vitest";

import { buildDiagnosticRows, type DiagSnapshot } from "./diagnostics";

function snap(over: Partial<DiagSnapshot> = {}): DiagSnapshot {
  return {
    gatewayUrl: "http://gw.test:8080",
    connection: "connected",
    healthz: "ok",
    readyz: "ok",
    bearerStored: true,
    claudeLocal: true,
    claudeOnGateway: "present",
    ...over,
  };
}

/** Finds a row by label. */
function row(rows: ReturnType<typeof buildDiagnosticRows>, label: string) {
  const r = rows.find((x) => x.label === label);
  if (!r) throw new Error(`no row "${label}"`);
  return r;
}

describe("buildDiagnosticRows", () => {
  it("all-good snapshot → every actionable row is ok", () => {
    const rows = buildDiagnosticRows(snap());
    expect(row(rows, "Connection").level).toBe("ok");
    expect(row(rows, "Gateway /healthz").level).toBe("ok");
    expect(row(rows, "Bearer token (editor)").level).toBe("ok");
    expect(row(rows, "Claude login (editor)").level).toBe("ok");
    expect(row(rows, "Claude login (gateway)")).toMatchObject({ value: "present", level: "ok" });
  });

  it("THE debug case: connected + local token but absent on gateway → warns to push", () => {
    const rows = buildDiagnosticRows(snap({ claudeOnGateway: "absent" }));
    const r = row(rows, "Claude login (gateway)");
    expect(r.level).toBe("warn");
    expect(r.value).toContain("Push Login to Gateway");
  });

  it("error connection → bad; unset gateway URL → bad", () => {
    expect(row(buildDiagnosticRows(snap({ connection: "error" })), "Connection").level).toBe("bad");
    expect(row(buildDiagnosticRows(snap({ gatewayUrl: "" })), "Gateway URL")).toMatchObject({
      value: "(unset)",
      level: "bad",
    });
  });

  it("missing secrets → warn with the right next action", () => {
    const rows = buildDiagnosticRows(snap({ bearerStored: false, claudeLocal: false }));
    expect(row(rows, "Bearer token (editor)")).toMatchObject({ level: "warn" });
    expect(row(rows, "Bearer token (editor)").value).toContain("Connect to Gateway");
    expect(row(rows, "Claude login (editor)").value).toContain("Log In");
  });

  it("gateway credential failure states map to bad", () => {
    for (const s of ["not-configured", "unauthorized", "unreachable"] as const) {
      expect(row(buildDiagnosticRows(snap({ claudeOnGateway: s })), "Claude login (gateway)").level).toBe("bad");
    }
  });

  it("failed reachability → bad; unknown (no URL probe) → warn", () => {
    expect(row(buildDiagnosticRows(snap({ healthz: "fail" })), "Gateway /healthz").level).toBe("bad");
    expect(row(buildDiagnosticRows(snap({ readyz: "unknown" })), "Gateway /readyz").level).toBe("warn");
  });

  it("no row ever contains a token-shaped value (model is presence-only)", () => {
    // Even when the snapshot is built from a state where secrets exist, the rows carry only
    // presence flags / enums — never a token value. (Labels like "Bearer token (editor)" name the
    // secret but carry no value.)
    const rows = buildDiagnosticRows(snap());
    // The VALUES (what's rendered as the row content) carry no token. Labels may name a secret
    // ("Bearer token (editor)") but never its value.
    const values = rows.map((r) => r.value).join(" | ");
    expect(values).not.toContain("sk-ant");
    expect(values).not.toMatch(/Bearer\s+\S+/);
  });
});
