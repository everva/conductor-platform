import { describe, expect, it } from "vitest";
import { holdoutIdFromRef } from "./holdoutRef.ts";

describe("holdoutIdFromRef", () => {
  it("extracts the id from pg:// and store:// holdout locators", () => {
    expect(holdoutIdFromRef("pg://holdouts/A-1")).toBe("A-1");
    expect(holdoutIdFromRef("store://holdouts/A-1/holdout_test.go")).toBe("A-1");
    expect(holdoutIdFromRef("  pg://holdouts/W-HEALTHZ  ")).toBe("W-HEALTHZ");
  });
  it("returns '' for a non-holdouts ref", () => {
    expect(holdoutIdFromRef("private:repo#x")).toBe("");
    expect(holdoutIdFromRef("")).toBe("");
  });
});
