// Unit tests for the pure formatting helpers: human age, heartbeat freshness
// classification (fresh / stale / never), and short time fallbacks.
import { describe, expect, it } from "vitest";
import { heartbeatFreshness, humanAge, shortTime } from "./format.ts";

describe("humanAge", () => {
  it("renders seconds / minutes / hours / days", () => {
    expect(humanAge(12)).toBe("12s ago");
    expect(humanAge(180)).toBe("3m ago");
    expect(humanAge(3600 * 2)).toBe("2h ago");
    expect(humanAge(86400)).toBe("1d ago");
  });
  it("clamps negatives to 0s", () => {
    expect(humanAge(-5)).toBe("0s ago");
  });
});

describe("heartbeatFreshness", () => {
  it("classifies a recent heartbeat as fresh", () => {
    expect(heartbeatFreshness(12, "2026-06-18T00:00:00Z")).toEqual({
      label: "12s ago",
      status: "fresh",
    });
  });
  it("classifies an old heartbeat as stale", () => {
    expect(heartbeatFreshness(120, "2026-06-18T00:00:00Z")).toEqual({
      label: "2m ago",
      status: "stale",
    });
  });
  it("classifies an absent heartbeat as never", () => {
    expect(heartbeatFreshness(0, undefined)).toEqual({ label: "never", status: "never" });
    expect(heartbeatFreshness(0, "")).toEqual({ label: "never", status: "never" });
  });
});

describe("shortTime", () => {
  it("returns a dash for empty input", () => {
    expect(shortTime(null)).toBe("—");
    expect(shortTime(undefined)).toBe("—");
    expect(shortTime("")).toBe("—");
    expect(shortTime("not-a-date")).toBe("—");
  });
});
