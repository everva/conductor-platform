// Conductor Platform — gateway auto-reconnect controller (M2).
//
// PROBLEM: the gateway link can drop (gateway restart, network blip, laptop sleep). Before this,
// the editor only re-established it on an explicit Reload / Connect — the ConnectionManager has no
// periodic restore and the EventsWatcher's WS onClose was a silent no-op, so a dropped link stayed
// dropped (the user saw "Conductor: disconnected" until they reloaded the window).
//
// THIS: a small, vscode-free, deterministic retry loop. It is KICKED when a loss is observed (a WS
// close, or a failed restore-on-activate while a token is stored) and then keeps attempting to
// restore the link with EXPONENTIAL BACKOFF until it reconnects (or is told to stop). On success it
// resets and goes idle; the existing ConnectionManager.onStateChange path does the rest (restart
// the watchers + refresh the trees). It owns no token and no network: the injected `attempt` closure
// performs the actual restore, so this stays a pure, fake-timer-testable scheduler.

/** Outcome of one reconnect attempt, returned by the injected `attempt` closure. */
export type AttemptResult =
  // Link is back — reset backoff + go idle (the onStateChange path handles the rest).
  | "connected"
  // Still down — schedule another attempt with a longer backoff.
  | "retry"
  // Do not loop (e.g. the user explicitly disconnected → no stored token). Go idle.
  | "stop";

/** Injectable timer seam so vitest drives the loop with a fake clock (no real setTimeout). VS
 * Code's global setTimeout/clearTimeout satisfy the default. */
export interface ReconnectScheduler {
  set(fn: () => void, ms: number): unknown;
  clear(handle: unknown): void;
}

const defaultScheduler: ReconnectScheduler = {
  set: (fn, ms) => setTimeout(fn, ms),
  clear: (h) => clearTimeout(h as ReturnType<typeof setTimeout>),
};

/** Observable phase, for the status mirror + tests. */
export type ReconnectPhase = "idle" | "scheduled" | "attempting";

export interface ReconnectDeps {
  /** Performs one restore attempt; returns whether the link is back / should keep trying. Never
   * throws (the controller treats a throw as "retry" defensively). */
  readonly attempt: () => Promise<AttemptResult>;
  /** Timer seam (defaults to setTimeout/clearTimeout). */
  readonly scheduler?: ReconnectScheduler;
  /** First backoff delay (ms). Default 1000. */
  readonly baseDelayMs?: number;
  /** Backoff ceiling (ms). Default 30000. */
  readonly maxDelayMs?: number;
  /** Optional phase observer (status mirror / telemetry / tests). */
  readonly onPhase?: (phase: ReconnectPhase) => void;
}

/**
 * Self-sustaining reconnect loop. Construct one per activation; call `kick()` whenever a link loss
 * is observed and `stop()` on dispose / explicit disconnect. Idempotent: kicking while already
 * scheduled or attempting is a no-op (no overlapping timers, no thundering retries).
 */
export class ReconnectController {
  readonly #attempt: () => Promise<AttemptResult>;
  readonly #scheduler: ReconnectScheduler;
  readonly #baseDelayMs: number;
  readonly #maxDelayMs: number;
  readonly #onPhase: ((phase: ReconnectPhase) => void) | undefined;

  #phase: ReconnectPhase = "idle";
  #failures = 0;
  #handle: unknown;

  constructor(deps: ReconnectDeps) {
    this.#attempt = deps.attempt;
    this.#scheduler = deps.scheduler ?? defaultScheduler;
    this.#baseDelayMs = deps.baseDelayMs ?? 1000;
    this.#maxDelayMs = deps.maxDelayMs ?? 30000;
    this.#onPhase = deps.onPhase;
  }

  /** Current phase (idle until kicked). */
  get phase(): ReconnectPhase {
    return this.#phase;
  }

  /** The backoff delay for the Nth (0-based) consecutive failure, capped. Pure; exported via the
   * instance for tests. delay = min(max, base * 2^failures). */
  delayFor(failures: number): number {
    const raw = this.#baseDelayMs * 2 ** failures;
    return Math.min(this.#maxDelayMs, raw);
  }

  /** Observe a link loss → start (or continue) the retry loop. No-op if already running. */
  kick(): void {
    if (this.#phase !== "idle") {
      return; // already scheduled or mid-attempt — don't stack timers.
    }
    this.#schedule();
  }

  /** Stop the loop (dispose / explicit disconnect): cancel any pending timer + reset backoff. */
  stop(): void {
    if (this.#handle !== undefined) {
      this.#scheduler.clear(this.#handle);
      this.#handle = undefined;
    }
    this.#failures = 0;
    this.#setPhase("idle");
  }

  #schedule(): void {
    const delay = this.delayFor(this.#failures);
    this.#setPhase("scheduled");
    this.#handle = this.#scheduler.set(() => {
      this.#handle = undefined;
      void this.#run();
    }, delay);
  }

  async #run(): Promise<void> {
    this.#setPhase("attempting");
    let result: AttemptResult;
    try {
      result = await this.#attempt();
    } catch {
      result = "retry"; // a throwing attempt is treated as still-down, defensively.
    }
    // A stop() during the in-flight attempt wins: don't reschedule over a deliberate halt.
    if (this.#phase !== "attempting") {
      return;
    }
    switch (result) {
      case "connected":
        this.#failures = 0;
        this.#setPhase("idle");
        return;
      case "stop":
        this.#failures = 0;
        this.#setPhase("idle");
        return;
      case "retry":
        this.#failures += 1;
        this.#schedule();
        return;
    }
  }

  #setPhase(next: ReconnectPhase): void {
    this.#phase = next;
    this.#onPhase?.(next);
  }
}
