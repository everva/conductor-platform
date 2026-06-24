// Tests for the native dispatch model (Faz-R). Pure: no vscode, no network. Pins the slug, the
// YAML synthesis (well-formed + safely quoted, matching the gateway's intake schema), tier parsing,
// acceptance splitting, the holdout default, and the {created,skipped} → message mapping.
import { describe, expect, it } from "vitest";
import {
  TIER_CHOICES,
  parseTier,
  slugifyId,
  defaultHoldoutRef,
  parseAcceptance,
  buildIntakeYaml,
  dispatchOutcome,
  type DispatchSpec,
} from "./dispatch";

describe("parseTier", () => {
  it("reads the bare tier token out of a choice label", () => {
    expect(parseTier(TIER_CHOICES[0])).toBe("T1");
    expect(parseTier(TIER_CHOICES[3])).toBe("T4");
    expect(parseTier("T2 — standard change")).toBe("T2");
  });
  it("rejects non-tier / undefined input", () => {
    expect(parseTier(undefined)).toBeUndefined();
    expect(parseTier("nope")).toBeUndefined();
    expect(parseTier("T9")).toBeUndefined();
  });
});

describe("slugifyId", () => {
  it("derives an upper-cased, dash-joined W-id from a title", () => {
    expect(slugifyId("Add a /healthz endpoint")).toBe("W-ADD-A-HEALTHZ-ENDPOINT");
    expect(slugifyId("  trailing  ")).toBe("W-TRAILING");
  });
  it("falls back for empty/symbol-only titles, and caps length without a trailing dash", () => {
    expect(slugifyId("")).toBe("W-task");
    expect(slugifyId("!!!")).toBe("W-task");
    const long = slugifyId("x".repeat(80));
    expect(long.startsWith("W-")).toBe(true);
    expect(long.endsWith("-")).toBe(false);
    expect(long.length).toBeLessThanOrEqual(2 + 32);
  });
});

describe("defaultHoldoutRef", () => {
  it("builds a repo-external store:// locator keyed by id (ADR-0018)", () => {
    expect(defaultHoldoutRef("W-1")).toBe("store://holdouts/W-1/holdout_test.go");
    expect(defaultHoldoutRef("")).toBe("store://holdouts/task/holdout_test.go");
  });
});

describe("parseAcceptance", () => {
  it("splits on newlines and ';', trims, and drops empties", () => {
    expect(parseAcceptance("a; b\n c ;; ")).toEqual(["a", "b", "c"]);
    expect(parseAcceptance("only one")).toEqual(["only one"]);
    expect(parseAcceptance("")).toEqual([]);
    expect(parseAcceptance(undefined)).toEqual([]);
  });
});

describe("buildIntakeYaml", () => {
  const spec: DispatchSpec = {
    id: "W-1",
    title: "Ship: the helper",
    lane: "backend",
    tier: "T2",
    acceptance: ["package exposes Feature()", "hidden holdout green"],
    holdoutRef: "store://holdouts/W-1/holdout_test.go",
  };

  it("renders a well-formed, double-quoted scenario document the gateway schema accepts", () => {
    expect(buildIntakeYaml(spec)).toBe(
      [
        'id: "W-1"',
        'title: "Ship: the helper"',
        'lane: "backend"',
        'tier: "T2"',
        "acceptance:",
        '  - "package exposes Feature()"',
        '  - "hidden holdout green"',
        'hidden_holdout_ref: "store://holdouts/W-1/holdout_test.go"',
        "",
      ].join("\n"),
    );
  });

  it("escapes quotes/backslashes/newlines so an odd title can't break the document", () => {
    const yaml = buildIntakeYaml({ ...spec, title: 'a "quote" and \\ slash' });
    expect(yaml).toContain('title: "a \\"quote\\" and \\\\ slash"');
  });

  it("emits a deps block only when deps are present", () => {
    expect(buildIntakeYaml(spec)).not.toContain("deps:");
    const withDeps = buildIntakeYaml({ ...spec, deps: ["X-1", "X-2"] });
    expect(withDeps).toContain("deps:\n  - \"X-1\"\n  - \"X-2\"");
  });
});

describe("dispatchOutcome", () => {
  it("summarizes created ids and offers them as the follow-up targets", () => {
    expect(dispatchOutcome({ created: ["W-1"], skipped: [] })).toEqual({
      message: "Dispatched 1 task: W-1.",
      createdIds: ["W-1"],
    });
    expect(dispatchOutcome({ created: ["W-1", "W-2"], skipped: ["W-old"] })).toEqual({
      message: "Dispatched 2 tasks: W-1, W-2 (skipped W-old).",
      createdIds: ["W-1", "W-2"],
    });
  });

  it("handles a duplicate-only (all skipped) and an empty result", () => {
    expect(dispatchOutcome({ created: [], skipped: ["W-1"] })).toEqual({
      message: "Already in the ledger (skipped): W-1.",
      createdIds: [],
    });
    expect(dispatchOutcome({ created: [], skipped: [] }).createdIds).toEqual([]);
    expect(dispatchOutcome(null).createdIds).toEqual([]);
    expect(dispatchOutcome("garbage").createdIds).toEqual([]);
  });
});
