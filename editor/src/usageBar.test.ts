import { describe, it, expect } from "vitest";
import { usageBarText } from "./extension";
import type { UsageSnapshot } from "./fleetReadClient";

describe("usageBarText", () => {
  it("empty map → hidden (text '')", () => {
    expect(usageBarText({}).text).toBe("");
  });
  it("renders per-subscription 5h % + tooltip; unavailable shows —", () => {
    const usage: Record<string, UsageSnapshot> = {
      admin: { label: "admin (sub#1)", available: false, reason: "token lacks user:profile scope" },
      vendor: { label: "vendor (sub#2)", available: true, five_hour: { used_pct: 16, resets_at: "2026-07-02T17:40:00Z" }, seven_day: { used_pct: 32, resets_at: null } },
    };
    const { text, tooltip } = usageBarText(usage);
    expect(text).toContain("vendor 16%");
    expect(text).toContain("admin —");
    expect(text.startsWith("$(pulse)")).toBe(true);
    expect(tooltip).toContain("vendor (sub#2): 5h 16%");
    expect(tooltip).toContain("resets 17:40");
    expect(tooltip).toContain("unavailable");
  });
});
