// Deterministic, headless tests for the ConnectionManager (4B-1). No vscode, no
// network: an in-memory SecretStore + a fake GatewayProbe drive every connect /
// disconnect / restore branch. The final test is the TOKEN-LEAK GUARD — the hard proof
// that the bearer token never escapes into a state value or a connect result (it may
// live ONLY in SecretStorage and the probe's Authorization header).
import { describe, expect, it } from "vitest";
import {
  ConnectionManager,
  type ConnectionState,
  type ConnectResult,
  type SecretStore,
} from "./connection";
import { GATEWAY_TOKEN_KEY, type GatewayProbe, type TokenCheck } from "./gateway";

function makeSecretStore(initial: Record<string, string> = {}): {
  store: SecretStore;
  map: Map<string, string>;
} {
  const map = new Map<string, string>(Object.entries(initial));
  return {
    map,
    store: {
      get: (k) => Promise.resolve(map.get(k)),
      store: (k, v) => {
        map.set(k, v);
        return Promise.resolve();
      },
      delete: (k) => {
        map.delete(k);
        return Promise.resolve();
      },
    },
  };
}

function makeProbe(opts: {
  ready?: { ok: boolean; status: number };
  check?: TokenCheck;
  seenTokens?: string[];
}): GatewayProbe {
  return {
    pingReadyz: () => Promise.resolve(opts.ready ?? { ok: true, status: 200 }),
    validateToken: (token) => {
      opts.seenTokens?.push(token);
      return Promise.resolve(opts.check ?? "valid");
    },
  };
}

describe("ConnectionManager.connect", () => {
  it("stores the token and connects only on (readyz ok + valid)", async () => {
    const secrets = makeSecretStore();
    const states: ConnectionState[] = [];
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ ready: { ok: true, status: 200 }, check: "valid" }),
      onStateChange: (s) => states.push(s),
    });

    const res = await m.connect("tok-good");
    expect(res).toEqual({ ok: true });
    expect(m.state).toBe("connected");
    expect(secrets.map.get(GATEWAY_TOKEN_KEY)).toBe("tok-good");
    expect(states).toEqual(["connecting", "connected"]);
  });

  it("does NOT store on a gateway that is not ready", async () => {
    const secrets = makeSecretStore();
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ ready: { ok: false, status: 503 } }),
    });
    const res = await m.connect("tok");
    expect(res).toEqual({ ok: false, reason: "gateway-unreachable" });
    expect(m.state).toBe("error");
    expect(secrets.map.has(GATEWAY_TOKEN_KEY)).toBe(false);
  });

  it("does NOT store on an unauthorized token (invalid-token)", async () => {
    const secrets = makeSecretStore();
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ ready: { ok: true, status: 200 }, check: "unauthorized" }),
    });
    const res = await m.connect("tok-bad");
    expect(res).toEqual({ ok: false, reason: "invalid-token" });
    expect(m.state).toBe("error");
    expect(secrets.map.has(GATEWAY_TOKEN_KEY)).toBe(false);
  });

  it("does NOT store when token validation is unreachable", async () => {
    const secrets = makeSecretStore();
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ ready: { ok: true, status: 200 }, check: "unreachable" }),
    });
    const res = await m.connect("tok");
    expect(res).toEqual({ ok: false, reason: "gateway-unreachable" });
    expect(secrets.map.has(GATEWAY_TOKEN_KEY)).toBe(false);
  });
});

describe("ConnectionManager.disconnect", () => {
  it("deletes the stored token and goes disconnected", async () => {
    const secrets = makeSecretStore({ [GATEWAY_TOKEN_KEY]: "tok" });
    const m = new ConnectionManager({ secrets: secrets.store, gateway: makeProbe({}) });
    await m.disconnect();
    expect(secrets.map.has(GATEWAY_TOKEN_KEY)).toBe(false);
    expect(m.state).toBe("disconnected");
  });
});

describe("ConnectionManager.restore", () => {
  it("with no stored token → disconnected (no validation)", async () => {
    const secrets = makeSecretStore();
    const seen: string[] = [];
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ seenTokens: seen }),
    });
    await m.restore();
    expect(m.state).toBe("disconnected");
    expect(seen).toHaveLength(0);
  });

  it("with a valid stored token → connected, token kept", async () => {
    const secrets = makeSecretStore({ [GATEWAY_TOKEN_KEY]: "tok" });
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ check: "valid" }),
    });
    await m.restore();
    expect(m.state).toBe("connected");
    expect(secrets.map.get(GATEWAY_TOKEN_KEY)).toBe("tok");
  });

  it("with a definitively-rejected token (401) → token DELETED, disconnected", async () => {
    const secrets = makeSecretStore({ [GATEWAY_TOKEN_KEY]: "stale" });
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ check: "unauthorized" }),
    });
    await m.restore();
    expect(m.state).toBe("disconnected");
    expect(secrets.map.has(GATEWAY_TOKEN_KEY)).toBe(false);
  });

  it("with a transient outage (unreachable) → token KEPT, disconnected", async () => {
    const secrets = makeSecretStore({ [GATEWAY_TOKEN_KEY]: "tok" });
    const m = new ConnectionManager({
      secrets: secrets.store,
      gateway: makeProbe({ check: "unreachable" }),
    });
    await m.restore();
    expect(m.state).toBe("disconnected");
    expect(secrets.map.get(GATEWAY_TOKEN_KEY)).toBe("tok");
  });
});

describe("token-leak guard", () => {
  it("never lets the token escape into a state value or a connect result", async () => {
    const SECRET = "ghp_SECRET_must_not_leak_4b1";
    const seenStates: ConnectionState[] = [];
    const results: ConnectResult[] = [];
    const seenByProbe: string[] = [];

    // Run every flow that touches the token, collecting all observable outputs.
    // Success path:
    {
      const secrets = makeSecretStore();
      const m = new ConnectionManager({
        secrets: secrets.store,
        gateway: makeProbe({ check: "valid", seenTokens: seenByProbe }),
        onStateChange: (s) => seenStates.push(s),
      });
      results.push(await m.connect(SECRET));
      await m.restore(); // re-validate the now-stored token
      await m.disconnect();
      // Sanity: the token DID reach the only two allowed sinks (store + probe header),
      // so the guard below is non-vacuous.
      expect(seenByProbe).toContain(SECRET);
    }
    // Failure paths:
    for (const check of ["unauthorized", "unreachable"] as const) {
      const secrets = makeSecretStore();
      const m = new ConnectionManager({
        secrets: secrets.store,
        gateway: makeProbe({ check, seenTokens: seenByProbe }),
        onStateChange: (s) => seenStates.push(s),
      });
      results.push(await m.connect(SECRET));
    }

    // The token must appear in NONE of the observable outputs (states + results).
    for (const s of seenStates) {
      expect(s).not.toContain(SECRET);
    }
    expect(JSON.stringify(results)).not.toContain(SECRET);
  });
});
