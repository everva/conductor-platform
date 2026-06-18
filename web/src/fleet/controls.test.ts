// Unit tests for the pure control-surface helpers (3B-3): the awaiting-approval
// predicate / selector and the ApiError → notice mapping. No React, no network.
import { describe, expect, it } from "vitest";
import { ApiError } from "../api/client.ts";
import {
  awaitingTasks,
  classifyControlError,
  isAwaitingApproval,
} from "./controls.ts";
import type { Task } from "../api/types.ts";

function task(over: Partial<Task>): Task {
  return {
    id: "t",
    project_id: "p",
    lane: "l",
    tier: "core",
    status: "queued",
    requires: [],
    deps: [],
    branch: "b",
    scenario_id: "",
    retry_count: 0,
    abort_requested: false,
    approved: false,
    ...over,
  };
}

describe("isAwaitingApproval", () => {
  it("is true for a held, unapproved task", () => {
    expect(isAwaitingApproval(task({ status: "awaiting-approval" }))).toBe(true);
  });
  it("is false once the task is approved", () => {
    expect(
      isAwaitingApproval(task({ status: "awaiting-approval", approved: true })),
    ).toBe(false);
  });
  it("is false for a running task", () => {
    expect(isAwaitingApproval(task({ status: "running" }))).toBe(false);
  });
});

describe("awaitingTasks", () => {
  it("returns only held tasks, sorted by id", () => {
    const got = awaitingTasks([
      task({ id: "t-b", status: "awaiting-approval" }),
      task({ id: "t-a", status: "awaiting-approval" }),
      task({ id: "t-c", status: "running" }),
    ]);
    expect(got.map((t) => t.id)).toEqual(["t-a", "t-b"]);
  });
  it("tolerates undefined", () => {
    expect(awaitingTasks(undefined)).toEqual([]);
  });
});

describe("classifyControlError", () => {
  it("flags 401 as unauthorized", () => {
    const out = classifyControlError(new ApiError(401, "bad token"));
    expect(out.unauthorized).toBe(true);
    expect(out.notice.tone).toBe("error");
  });
  it("maps 409 to a benign warn carrying the gateway message", () => {
    const out = classifyControlError(new ApiError(409, "no task running to abort"));
    expect(out.unauthorized).toBe(false);
    expect(out.notice.tone).toBe("warn");
    expect(out.notice.message).toBe("no task running to abort");
  });
  it("maps other statuses to an error notice including the message", () => {
    const out = classifyControlError(new ApiError(500, "boom"));
    expect(out.notice.tone).toBe("error");
    expect(out.notice.message).toContain("500");
    expect(out.notice.message).toContain("boom");
  });
  it("maps a non-ApiError to a generic reach error", () => {
    const out = classifyControlError(new Error("network down"));
    expect(out.unauthorized).toBe(false);
    expect(out.notice.tone).toBe("error");
    expect(out.notice.message).toMatch(/could not reach/i);
  });
});
