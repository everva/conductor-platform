// Live events for the conductor-api gateway (ADR-0025, task 3A-2): the historical
// replay endpoint GET /events (over the additive events.EventReader seam) and the
// realtime push endpoint GET /ws (a WebSocket over events.EventBus.Subscribe).
//
// Both surfaces are read-only over the event stream and bearer-authenticated. They
// share one query-param → events.Filter parser so the history a UI backfills and
// the live stream it then follows use IDENTICAL filtering semantics. Like the rest
// of the gateway they stay secret-free: the token is never logged or echoed, and
// the ?token= WS fallback (below) is compared constant-time and never emitted.
package main

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/everva/conductor-platform/internal/events"
)

// wsWriteTimeout bounds a single event write to a WebSocket client so one stuck
// (non-draining) client cannot wedge the per-connection push loop forever — on
// timeout the connection is torn down and the subscription released.
const wsWriteTimeout = 10 * time.Second

// parseEventFilter builds an events.Filter from the shared query params used by
// BOTH GET /events and GET /ws: project, task, phase, kind, intervention. phase
// and kind, when present, are validated against the frozen vocabulary so a typo
// fails fast rather than silently matching nothing. On an invalid phase/kind it
// returns a short, non-secret error message suitable for a 400 body.
func parseEventFilter(q map[string][]string) (events.Filter, string, bool) {
	get := func(k string) string {
		if v, ok := q[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}

	f := events.Filter{
		Project: get("project"),
		Task:    get("task"),
	}

	if p := get("phase"); p != "" {
		ph := events.Phase(p)
		if !ph.Valid() {
			return events.Filter{}, "invalid phase", false
		}
		f.Phase = ph
	}
	if k := get("kind"); k != "" {
		kn := events.Kind(k)
		if !kn.Valid() {
			return events.Filter{}, "invalid kind", false
		}
		f.Kind = kn
	}
	f.InterventionOnly = isTruthy(get("intervention"))

	return f, "", true
}

// isTruthy reports whether a query value means "on" (1/true, case-insensitive).
func isTruthy(v string) bool {
	switch v {
	case "1", "true", "TRUE", "True":
		return true
	default:
		return false
	}
}

// handleEvents: GET /events → JSON array of historical events matching the query
// filter, with TS >= since, capped at limit. It uses the EventReader seam; when
// the configured bus does not implement it (never, for the two real impls) it
// returns 501 rather than panicking. Empty result serializes as [] (never null).
func (s *apiServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	if s.reader == nil {
		writeError(w, http.StatusNotImplemented, "event history not supported")
		return
	}

	q := r.URL.Query()
	filter, msg, ok := parseEventFilter(q)
	if !ok {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// since: empty = zero time (from the beginning); invalid → 400.
	var since time.Time
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid since")
			return
		}
		since = t
	}

	// limit: empty/<=0 → reader default; non-integer → 400. The reader clamps.
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}

	evs, err := s.reader.ListEvents(r.Context(), filter, since, limit)
	if err != nil {
		// Never surface the underlying error (could mention the DSN) — fixed string.
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Always a non-nil slice so an empty history encodes "[]" not "null".
	out := make([]events.Event, 0, len(evs))
	out = append(out, evs...)
	writeJSON(w, http.StatusOK, out)
}

// handleWS: GET /ws → upgrade to a WebSocket and push every matching live event
// to the client until it disconnects, the bus closes, or the request ctx ends.
//
// Auth is enforced BEFORE the upgrade (a rejected client gets a plain 401, never
// an upgraded socket). Because the browser WebSocket API cannot set an
// Authorization header, the bearer token is accepted via EITHER the
// "Authorization: Bearer <t>" header (non-browser clients) OR a ?token=<t> query
// param (browser fallback). The query token rides the URL, so this endpoint is
// only safe over TLS; the gateway NEVER logs the token or the URL, and keeping it
// that way is load-bearing for this fallback (see also handleWS's no-logging).
func (s *apiServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if !s.wsAuthorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	filter, msg, ok := parseEventFilter(r.URL.Query())
	if !ok {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// OriginPatterns: default (same-origin) for now. 3B/Ingress will configure the
	// allowed browser origins explicitly; leaving the default here means a
	// cross-origin browser dial is rejected by coder/websocket until then.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// No per-message compression — events are small JSON and compression adds
		// CPU + a documented context-takeover footgun we don't need.
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept already wrote the HTTP error to w; nothing more to do.
		return
	}
	// Status 1011 (internal error) is the catch-all close on an unexpected exit;
	// the normal-close path below overrides it.
	defer func() { _ = conn.Close(websocket.StatusInternalError, "closing") }()

	ctx := r.Context()

	// Subscribe to the SAME bus the daemon publishes on. The channel closes on
	// cancel/ctx-done/bus-close; cancel is always called on exit.
	ch, cancel, err := s.bus.Subscribe(ctx, filter)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "subscribe failed")
		return
	}
	defer cancel()

	// CloseRead drains/handles client control frames (close, ping) in the
	// background and cancels the returned ctx when the client goes away, so a
	// client-initiated close is detected promptly even while we're blocked on the
	// event channel. We never read application data from the client on /ws.
	ctx = conn.CloseRead(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-ch:
			if !open {
				// Bus closed or subscription cancelled → end the stream cleanly.
				_ = conn.Close(websocket.StatusNormalClosure, "stream closed")
				return
			}
			if err := s.writeEvent(ctx, conn, ev); err != nil {
				// Write failed/timed out → tear down; defer closes the conn.
				return
			}
		}
	}
}

// writeEvent marshals and pushes one event as a JSON text frame, bounded by
// wsWriteTimeout so a stuck client can't block the loop indefinitely.
func (s *apiServer) writeEvent(ctx context.Context, conn *websocket.Conn, ev events.Event) error {
	wctx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
	defer cancel()
	return wsjson.Write(wctx, conn, ev)
}

// wsAuthorized validates the bearer token for the /ws route, accepting it from
// EITHER the Authorization header OR the ?token= query param (browser fallback,
// see handleWS). The comparison is constant-time; neither source is logged. A
// missing/empty/wrong token returns false (the caller replies 401 BEFORE upgrade).
func (s *apiServer) wsAuthorized(r *http.Request) bool {
	const prefix = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(prefix) && h[:len(prefix)] == prefix {
		if subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(s.token)) == 1 {
			return true
		}
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1 {
			return true
		}
	}
	return false
}

// asReader returns bus as an EventReader when it implements the additive seam,
// else nil. The two real bus impls (MemoryBus, PostgresBus) both implement it; a
// nil result drives the 501 guard in handleEvents rather than a panic.
func asReader(bus events.EventBus) events.EventReader {
	if r, ok := bus.(events.EventReader); ok {
		return r
	}
	return nil
}
