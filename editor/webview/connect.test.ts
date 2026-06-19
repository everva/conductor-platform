// Unit test for the FORK-MODE client wiring (Faz-4 DALGA 4B, 4B-3). connect.ts is
// deliberately React-free and alias-free (it takes the ApiClient ctor + transport by
// injection), so it runs in the host's vitest WITHOUT pulling in React or the @cockpit
// source tree. The full React render of the cockpit is verified by typecheck + esbuild
// build (ADR-0029), not here.
import { describe, expect, it, vi } from "vitest";

import { forkClientFactories, type HttpTransportLike } from "./connect";

// A stub HTTP transport (never called here — we only assert wiring, not I/O).
const http: HttpTransportLike = {
  send: vi.fn().mockResolvedValue({ status: 200, ok: true, body: "" }),
};

describe("forkClientFactories", () => {
  it("returns four zero-arg factories, each constructing a client over the SAME transport", () => {
    // Records the config each construction received so we can assert the transport wiring.
    const seen: Array<{ transport: HttpTransportLike }> = [];
    class FakeApiClient {
      readonly transport: HttpTransportLike;
      constructor(config: { transport: HttpTransportLike }) {
        seen.push(config);
        this.transport = config.transport;
      }
    }

    const { makeClient, makeControlClient, makeIntakeClient, makeHistory } = forkClientFactories(
      FakeApiClient,
      http,
    );

    const a = makeClient();
    const b = makeControlClient();
    const c = makeIntakeClient();
    const d = makeHistory();

    // Each factory built a client...
    expect(a).toBeInstanceOf(FakeApiClient);
    expect(b).toBeInstanceOf(FakeApiClient);
    expect(c).toBeInstanceOf(FakeApiClient);
    expect(d).toBeInstanceOf(FakeApiClient);
    // ...injected with the one shared transport (fork: token-free, host owns auth).
    expect(seen).toHaveLength(4);
    for (const config of seen) {
      expect(config.transport).toBe(http);
    }
    expect(a.transport).toBe(http);
  });

  it("ignores any token arg (factories are zero-arg but assignable to (token) => …)", () => {
    class FakeApiClient {
      constructor(public readonly config: { transport: HttpTransportLike }) {}
    }
    const { makeClient } = forkClientFactories(FakeApiClient, http);
    // Assignable to the dashboard's (token: string) => Client prop; the token is dropped.
    const asTokenFactory: (token: string) => FakeApiClient = makeClient;
    expect(asTokenFactory("ignored-token").config.transport).toBe(http);
  });
});
