// Conductor Platform — connection/auth manager (Faz-4 DALGA 4B, 4B-1).
//
// SCOPE (4B-1): owns the gateway connection lifecycle (connect / disconnect /
// restore-on-activate) and the bearer token's at-rest home, VS Code SecretStorage.
// It is the ONLY place the token is read/written. It depends on two narrow,
// injectable seams so vitest can drive it with no `vscode` and no real network:
//   - `SecretStore`  — get/store/delete a string by key (vscode `SecretStorage`
//                       structurally satisfies it; tests pass an in-memory map);
//   - `GatewayProbe` — pingReadyz + validateToken bound to a base url (gateway.ts's
//                       `makeGatewayProbe`; tests pass a fake).
//
// TOKEN DISCIPLINE (HARD): the token lives ONLY in SecretStorage (its at-rest home)
// and, transiently, in the Authorization header `GatewayProbe.validateToken` sends. It
// is NEVER cached on the instance and there is no getter; `state` and `ConnectResult`
// carry only closed enums. Nothing here logs, returns, or messages the token. (The host
// in extension.ts likewise keeps it out of every vscode message/log — see its tests.)
// When 4B-2 needs the token to proxy REST/WS for the webview it reads it from
// SecretStorage (the source of truth) rather than a field.
//
// RESTORE POLICY (documented choice): a transient gateway outage must NOT wipe a
// stored token. So `restore` clears the secret ONLY on a DEFINITIVE 401
// ("unauthorized" → the gateway actively rejected it). On "unreachable" the token is
// KEPT and state is left "disconnected" so the next connect/restore can succeed once
// the gateway is back. The only other thing that clears the secret is an explicit
// `disconnect()`.

import type { GatewayProbe } from "./gateway";
import { GATEWAY_TOKEN_KEY } from "./gateway";

/**
 * Minimal at-rest secret store. VS Code's `SecretStorage` satisfies this structurally
 * (its get/store/delete signatures match), so the host passes `context.secrets`
 * directly while tests pass an in-memory fake. Intentionally narrow: no `onDidChange`,
 * no enumeration — just the three operations the manager needs.
 */
export interface SecretStore {
  get(key: string): Thenable<string | undefined>;
  store(key: string, value: string): Thenable<void>;
  delete(key: string): Thenable<void>;
}

/** Connection lifecycle state surfaced to the UI (status bar / view). Never carries
 * the token. */
export type ConnectionState = "disconnected" | "connecting" | "connected" | "error";

/**
 * Closed result of a `connect` attempt. The `reason` is an enum so the host can pick
 * a user-facing (token-free) message; the raw token is never part of this value.
 */
export type ConnectResult =
  | { ok: true }
  | { ok: false; reason: "gateway-unreachable" | "invalid-token" };

/** Dependencies injected into the manager. `onStateChange` lets the host mirror state
 * onto a status-bar item / the Fleet view without the manager importing `vscode`. */
export interface ConnectionDeps {
  readonly secrets: SecretStore;
  readonly gateway: GatewayProbe;
  readonly onStateChange?: (state: ConnectionState) => void;
}

/**
 * Owns the gateway connection + the token at rest. Construct one per activation with
 * the real `context.secrets` + a `makeGatewayProbe(gatewayUrl)`; tests construct it
 * with fakes. The token never leaves this object except into SecretStorage and the
 * Authorization header sent by the probe.
 */
export class ConnectionManager {
  readonly #secrets: SecretStore;
  readonly #gateway: GatewayProbe;
  readonly #onStateChange: ((state: ConnectionState) => void) | undefined;

  #state: ConnectionState = "disconnected";

  constructor(deps: ConnectionDeps) {
    this.#secrets = deps.secrets;
    this.#gateway = deps.gateway;
    this.#onStateChange = deps.onStateChange;
  }

  /** Current lifecycle state. Read-only; mutated only via the flows below. */
  get state(): ConnectionState {
    return this.#state;
  }

  /**
   * Connect + persist. Order: state "connecting" → `pingReadyz` (gateway up?) →
   * `validateToken` (token good?). The token is stored ONLY when validation returns
   * "valid"; every failure path stores NOTHING and sets state "error".
   *   - readyz not ok            → "error", reason "gateway-unreachable", store nothing
   *   - validate "unauthorized"  → "error", reason "invalid-token",      store nothing
   *   - validate "unreachable"   → "error", reason "gateway-unreachable", store nothing
   *   - validate "valid"         → store token, state "connected", ok
   * The gateway base url is fixed at construction (the injected probe is bound to it),
   * so connect takes only the token.
   */
  async connect(token: string): Promise<ConnectResult> {
    this.#setState("connecting");

    const ready = await this.#gateway.pingReadyz();
    if (!ready.ok) {
      this.#setState("error");
      return { ok: false, reason: "gateway-unreachable" };
    }

    const check = await this.#gateway.validateToken(token);
    if (check === "unauthorized") {
      this.#setState("error");
      return { ok: false, reason: "invalid-token" };
    }
    if (check === "unreachable") {
      this.#setState("error");
      return { ok: false, reason: "gateway-unreachable" };
    }

    // check === "valid": persist and connect. This is the ONLY place we write the token.
    await this.#secrets.store(GATEWAY_TOKEN_KEY, token);
    this.#setState("connected");
    return { ok: true };
  }

  /** Explicit disconnect: forget the token (delete the secret + clear the cache) and
   * go "disconnected". This is one of only two paths that clear the stored token (the
   * other being a definitive 401 during `restore`). */
  async disconnect(): Promise<void> {
    await this.#secrets.delete(GATEWAY_TOKEN_KEY);
    this.#setState("disconnected");
  }

  /**
   * Silent restore-on-activate. Reads the stored token (if any) and re-validates it:
   *   - no stored token         → state "disconnected"
   *   - validate "valid"        → cache it, state "connected"
   *   - validate "unauthorized" → DELETE the stale token, state "disconnected"
   *   - validate "unreachable"  → KEEP the token (transient outage), state "disconnected"
   * Never throws, never logs/returns the token. See RESTORE POLICY in the file header.
   * The gateway base url is fixed at construction (the injected probe is bound to it).
   */
  async restore(): Promise<void> {
    const stored = await this.#secrets.get(GATEWAY_TOKEN_KEY);
    if (stored === undefined) {
      this.#setState("disconnected");
      return;
    }

    const check = await this.#gateway.validateToken(stored);
    if (check === "valid") {
      this.#setState("connected");
      return;
    }
    if (check === "unauthorized") {
      // Definitive rejection: the stored token is stale → clear it.
      await this.#secrets.delete(GATEWAY_TOKEN_KEY);
      this.#setState("disconnected");
      return;
    }
    // "unreachable": transient outage. Keep the token; just report not-connected.
    this.#setState("disconnected");
  }

  /** Sets state and fires the change callback (if any). Single mutation point so the
   * UI mirror can never miss a transition. */
  #setState(next: ConnectionState): void {
    this.#state = next;
    this.#onStateChange?.(next);
  }
}
