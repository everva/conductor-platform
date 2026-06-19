// Conductor Platform — real WsConnector (Faz-4 DALGA 4B, 4B-2).
//
// SCOPE (4B-2): the thin `ws` adapter that satisfies hostBridge.ts's `WsConnector`
// seam. VS Code's Node extension host has no stable global WebSocket, so the host opens
// gateway event streams via the `ws` npm package. This is the I/O EDGE — by design it is
// NOT unit-tested (there is nothing to assert beyond "it calls ws"); the bridge logic is
// covered by hostBridge.test.ts with a FAKE connector. Keep this file tiny so the
// untested surface stays trivially-correct-by-inspection.
//
// TOKEN NOTE: the token is already baked into `url` (the gateway `?token=` query) by the
// host before this is called; this adapter just opens the socket and forwards frames.

import WebSocket from "ws";
import type { WsConnector, WsHandle } from "./hostBridge";

/**
 * A `WsConnector` backed by the `ws` package. `open(url, h)` opens the socket and wires
 * its events onto the handler: open→onOpen, message→onMessage (decoded to a string),
 * close/error→onClose (an error is treated as a close so the bridge always sees exactly
 * one terminal signal). The returned handle closes the socket.
 */
export const wsConnector: WsConnector = {
  open(url, h): WsHandle {
    const sock = new WebSocket(url);
    sock.on("open", () => h.onOpen());
    sock.on("message", (data: WebSocket.RawData) => h.onMessage(data.toString()));
    sock.on("close", () => h.onClose());
    sock.on("error", () => h.onClose());
    return {
      close: () => sock.close(),
    };
  },
};
