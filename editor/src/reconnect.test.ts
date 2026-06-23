// Deterministic tests for the auto-reconnect controller (M2). A fake scheduler captures the
// pending timer (fn + delay) so the backoff loop is driven without real time. No vscode, no network.
import { describe, expect, it, vi } from "vitest";

import { ReconnectController, type AttemptResult, type ReconnectScheduler } from "./reconnect";

/** A fake scheduler holding ONE pending timer; `fire()` runs it and flushes the async attempt. */
function fakeScheduler() {
  let pending: { fn: () => void; ms: number } | undefined;
  const scheduler: ReconnectScheduler = {
    set: (fn, ms) => {
      pending = { fn, ms };
      return pending;
    },
    clear: () => {
      pending = undefined;
    },
  };
  return {
    scheduler,
    pendingDelay: () => pending?.ms,
    hasPending: () => pending !== undefined,
    async fire() {
      const p = pending;
      pending = undefined;
      p?.fn();
      // Flush the microtask chain the fired callback kicked off (#run is async).
      await new Promise((r) => setTimeout(r, 0));
    },
  };
}

describe("ReconnectController", () => {
  it("schedules with the base delay on kick, and goes idle when the attempt connects", async () => {
    const fs = fakeScheduler();
    const attempt = vi.fn<() => Promise<AttemptResult>>().mockResolvedValue("connected");
    const c = new ReconnectController({ attempt, scheduler: fs.scheduler, baseDelayMs: 1000, maxDelayMs: 30000 });

    c.kick();
    expect(c.phase).toBe("scheduled");
    expect(fs.pendingDelay()).toBe(1000);

    await fs.fire();
    expect(attempt).toHaveBeenCalledTimes(1);
    expect(c.phase).toBe("idle");
    expect(fs.hasPending()).toBe(false); // no reschedule after success
  });

  it("backs off exponentially on repeated retry, then resets after a success", async () => {
    const fs = fakeScheduler();
    const results: AttemptResult[] = ["retry", "retry", "connected"];
    let i = 0;
    const attempt = vi.fn<() => Promise<AttemptResult>>().mockImplementation(() => Promise.resolve(results[i++]!));
    const c = new ReconnectController({ attempt, scheduler: fs.scheduler, baseDelayMs: 1000, maxDelayMs: 30000 });

    c.kick();
    expect(fs.pendingDelay()).toBe(1000); // failures=0 → base

    await fs.fire(); // retry → failures=1
    expect(fs.pendingDelay()).toBe(2000); // base * 2^1

    await fs.fire(); // retry → failures=2
    expect(fs.pendingDelay()).toBe(4000); // base * 2^2

    await fs.fire(); // connected → idle
    expect(c.phase).toBe("idle");
    expect(fs.hasPending()).toBe(false);
    expect(attempt).toHaveBeenCalledTimes(3);
  });

  it("caps the backoff at maxDelayMs", () => {
    const fs = fakeScheduler();
    const c = new ReconnectController({ attempt: () => Promise.resolve("retry"), scheduler: fs.scheduler, baseDelayMs: 1000, maxDelayMs: 8000 });
    expect(c.delayFor(0)).toBe(1000);
    expect(c.delayFor(3)).toBe(8000); // 1000*8
    expect(c.delayFor(10)).toBe(8000); // capped, not 1024000
  });

  it("stops looping when the attempt returns 'stop' (e.g. user disconnected, no token)", async () => {
    const fs = fakeScheduler();
    const attempt = vi.fn<() => Promise<AttemptResult>>().mockResolvedValue("stop");
    const c = new ReconnectController({ attempt, scheduler: fs.scheduler });

    c.kick();
    await fs.fire();
    expect(c.phase).toBe("idle");
    expect(fs.hasPending()).toBe(false);
  });

  it("kick() is idempotent while scheduled (no stacked timers)", () => {
    const fs = fakeScheduler();
    const set = vi.spyOn(fs.scheduler, "set");
    const c = new ReconnectController({ attempt: () => Promise.resolve("retry"), scheduler: fs.scheduler });

    c.kick();
    c.kick();
    c.kick();
    expect(set).toHaveBeenCalledTimes(1); // only the first kick scheduled
  });

  it("stop() cancels a pending attempt and resets backoff", async () => {
    const fs = fakeScheduler();
    const attempt = vi.fn<() => Promise<AttemptResult>>().mockResolvedValue("retry");
    const c = new ReconnectController({ attempt, scheduler: fs.scheduler, baseDelayMs: 1000 });

    c.kick();
    await fs.fire(); // retry → failures=1, rescheduled at 2000
    expect(fs.pendingDelay()).toBe(2000);

    c.stop();
    expect(c.phase).toBe("idle");
    expect(fs.hasPending()).toBe(false);

    // After stop the backoff is reset: a fresh kick schedules at the base delay again.
    c.kick();
    expect(fs.pendingDelay()).toBe(1000);
  });

  it("treats a throwing attempt as 'retry' (stays resilient)", async () => {
    const fs = fakeScheduler();
    const attempt = vi.fn<() => Promise<AttemptResult>>().mockRejectedValue(new Error("boom"));
    const c = new ReconnectController({ attempt, scheduler: fs.scheduler, baseDelayMs: 1000 });

    c.kick();
    await fs.fire();
    // Did not crash; rescheduled with backoff.
    expect(c.phase).toBe("scheduled");
    expect(fs.pendingDelay()).toBe(2000);
  });
});
