// Deterministic, headless tests for the host↔webview message guards (4B-2). The guards
// are the trust boundary: postMessage delivers `unknown`, so each side validates before
// acting. These lock that valid messages pass and junk (null, wrong kind, missing/
// wrong-typed fields, bad filter values) is rejected.
import { describe, expect, it } from "vitest";
import { isHostMessage, isWebviewRequest, isHostNavigate, isWebviewReady } from "./protocol";

describe("isWebviewRequest", () => {
  it("accepts every valid WebviewRequest kind (including optional fields)", () => {
    expect(isWebviewRequest({ kind: "rest-request", id: "b1", method: "GET", path: "/x" })).toBe(
      true,
    );
    expect(
      isWebviewRequest({
        kind: "rest-request",
        id: "b1",
        method: "POST",
        path: "/x",
        body: "{}",
        contentType: "application/json",
      }),
    ).toBe(true);
    expect(isWebviewRequest({ kind: "event-subscribe", id: "b2" })).toBe(true);
    expect(
      isWebviewRequest({
        kind: "event-subscribe",
        id: "b2",
        filter: { project: "p", limit: 10, intervention: true },
      }),
    ).toBe(true);
    expect(isWebviewRequest({ kind: "event-unsubscribe", id: "b3" })).toBe(true);
  });

  it("rejects non-objects and missing/blank id", () => {
    expect(isWebviewRequest(null)).toBe(false);
    expect(isWebviewRequest(undefined)).toBe(false);
    expect(isWebviewRequest("rest-request")).toBe(false);
    expect(isWebviewRequest(42)).toBe(false);
    expect(isWebviewRequest({ kind: "rest-request", method: "GET", path: "/x" })).toBe(false);
    expect(isWebviewRequest({ kind: "rest-request", id: 1, method: "GET", path: "/x" })).toBe(false);
  });

  it("rejects an unknown kind and rest-request with wrong-typed fields", () => {
    expect(isWebviewRequest({ kind: "nope", id: "b1" })).toBe(false);
    expect(isWebviewRequest({ kind: "rest-request", id: "b1", method: "GET" })).toBe(false); // no path
    expect(isWebviewRequest({ kind: "rest-request", id: "b1", path: "/x" })).toBe(false); // no method
    expect(
      isWebviewRequest({ kind: "rest-request", id: "b1", method: "GET", path: "/x", body: 5 }),
    ).toBe(false); // body wrong type
  });

  it("rejects an event-subscribe whose filter is not a flat primitive record", () => {
    expect(isWebviewRequest({ kind: "event-subscribe", id: "b2", filter: "p" })).toBe(false);
    expect(isWebviewRequest({ kind: "event-subscribe", id: "b2", filter: { a: {} } })).toBe(false);
    expect(
      isWebviewRequest({ kind: "event-subscribe", id: "b2", filter: { a: () => 0 } }),
    ).toBe(false);
  });
});

describe("isHostMessage", () => {
  it("accepts every valid HostMessage kind", () => {
    expect(isHostMessage({ kind: "rest-response", id: "b1", status: 200, ok: true, body: "{}" })).toBe(
      true,
    );
    expect(isHostMessage({ kind: "rest-error", id: "b1", message: "boom" })).toBe(true);
    expect(isHostMessage({ kind: "event-open", id: "b2" })).toBe(true);
    expect(isHostMessage({ kind: "event-message", id: "b2", data: "{}" })).toBe(true);
    expect(isHostMessage({ kind: "event-close", id: "b2" })).toBe(true);
  });

  it("rejects non-objects, missing id, unknown kind, and wrong-typed fields", () => {
    expect(isHostMessage(null)).toBe(false);
    expect(isHostMessage({ kind: "rest-response", status: 200, ok: true, body: "{}" })).toBe(false);
    expect(isHostMessage({ kind: "ghost", id: "b1" })).toBe(false);
    expect(isHostMessage({ kind: "rest-response", id: "b1", status: "200", ok: true, body: "" })).toBe(
      false,
    ); // status wrong type
    expect(isHostMessage({ kind: "rest-response", id: "b1", status: 200, ok: 1, body: "" })).toBe(
      false,
    ); // ok wrong type
    expect(isHostMessage({ kind: "rest-error", id: "b1" })).toBe(false); // no message
    expect(isHostMessage({ kind: "event-message", id: "b2" })).toBe(false); // no data
  });
});

describe("N3 control channel guards", () => {
  it("isHostNavigate accepts a well-formed navigate-session and rejects junk", () => {
    expect(isHostNavigate({ kind: "navigate-session", project: "p", task: "t" })).toBe(true);
    expect(isHostNavigate(null)).toBe(false);
    expect(isHostNavigate({ kind: "navigate-session", project: "p" })).toBe(false); // no task
    expect(isHostNavigate({ kind: "navigate-session", task: "t" })).toBe(false); // no project
    expect(isHostNavigate({ kind: "navigate-session", project: 1, task: "t" })).toBe(false);
    expect(isHostNavigate({ kind: "other", project: "p", task: "t" })).toBe(false);
  });

  it("isWebviewReady accepts the ready ping and rejects junk", () => {
    expect(isWebviewReady({ kind: "webview-ready" })).toBe(true);
    expect(isWebviewReady(null)).toBe(false);
    expect(isWebviewReady({ kind: "nope" })).toBe(false);
  });

  it("the control channel does NOT cross-contaminate the bridge routers (no `id` → rejected)", () => {
    // The navigate/ready messages carry no correlation id, so the REST/event guards reject
    // them — they can never be mistaken for a rest-response/event frame or a rest-request.
    expect(isHostMessage({ kind: "navigate-session", project: "p", task: "t" })).toBe(false);
    expect(isWebviewRequest({ kind: "webview-ready" })).toBe(false);
    // …and the REST/event messages are not mistaken for control messages either.
    expect(isHostNavigate({ kind: "rest-response", id: "b1", status: 200, ok: true, body: "{}" })).toBe(
      false,
    );
    expect(isWebviewReady({ kind: "rest-request", id: "b1", method: "GET", path: "/x" })).toBe(false);
  });
});
