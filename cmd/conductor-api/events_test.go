package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/statestore"
)

// eventsBaseTS is the deterministic base timestamp for seeded events.
var eventsBaseTS = time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)

// newEventsServer builds an apiServer over a MemoryBus (deterministic clock +
// IDs) and an empty in-memory store, suitable for the /events and /ws tests. It
// returns the server and the bus so a test can Publish through the SAME bus the
// server subscribes to / reads from.
func newEventsServer(t *testing.T) (*apiServer, *events.MemoryBus) {
	t.Helper()
	var n int
	bus := events.NewMemoryBus(
		events.WithClock(func() time.Time { return eventsBaseTS }),
		events.WithIDFunc(func() string {
			n++
			return "ev-" + strconv.Itoa(n)
		}),
		events.WithBufferSize(256),
	)
	t.Cleanup(bus.Close)
	s := &apiServer{
		store:  statestore.NewMemoryStore(),
		bus:    bus,
		reader: bus,
		token:  testToken,
		clock:  fixedClock,
	}
	return s, bus
}

// publishAt publishes an event at an explicit TS through the bus (history is
// retained for ListEvents). It fails the test on a publish error.
func publishAt(t *testing.T, bus *events.MemoryBus, ts time.Time, ev events.Event) {
	t.Helper()
	ev.TS = ts
	if err := bus.Publish(context.Background(), ev); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

// --- GET /events ---

func seedEventsHistory(t *testing.T, bus *events.MemoryBus) {
	t.Helper()
	publishAt(t, bus, eventsBaseTS, events.Event{
		Project: "proj-a", Task: "T-1", Phase: events.PhasePlan, Kind: events.KindStarted,
	})
	publishAt(t, bus, eventsBaseTS.Add(1*time.Minute), events.Event{
		Project: "proj-a", Task: "T-2", Phase: events.PhaseDevelop, Kind: events.KindLog,
	})
	publishAt(t, bus, eventsBaseTS.Add(2*time.Minute), events.Event{
		Project: "proj-b", Task: "T-9", Phase: events.PhaseVerify, Kind: events.KindInterventionNeeded,
	})
	publishAt(t, bus, eventsBaseTS.Add(3*time.Minute), events.Event{
		Project: "proj-a", Task: "T-1", Phase: events.PhaseMerge, Kind: events.KindMerge,
	})
}

func getEvents(t *testing.T, s *apiServer, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/events"+query, nil)
	req.Header.Set("Authorization", bearer())
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

func decodeEvents(t *testing.T, rec *httptest.ResponseRecorder) []events.Event {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out []events.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	return out
}

func TestEventsAllAscending(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	got := decodeEvents(t, getEvents(t, s, ""))
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].TS.Before(got[i-1].TS) {
			t.Errorf("not ascending at %d", i)
		}
	}
}

func TestEventsFilterByProject(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	got := decodeEvents(t, getEvents(t, s, "?project=proj-b"))
	if len(got) != 1 || got[0].Project != "proj-b" {
		t.Fatalf("project filter = %+v, want 1 proj-b", got)
	}
}

func TestEventsFilterByTask(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	got := decodeEvents(t, getEvents(t, s, "?task=T-1"))
	if len(got) != 2 {
		t.Fatalf("task filter len = %d, want 2", len(got))
	}
	for _, e := range got {
		if e.Task != "T-1" {
			t.Errorf("got task %q, want T-1", e.Task)
		}
	}
}

func TestEventsFilterByKind(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	got := decodeEvents(t, getEvents(t, s, "?kind=log"))
	if len(got) != 1 || got[0].Kind != events.KindLog {
		t.Fatalf("kind filter = %+v, want 1 log", got)
	}
}

func TestEventsFilterByPhase(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	got := decodeEvents(t, getEvents(t, s, "?phase=merge"))
	if len(got) != 1 || got[0].Phase != events.PhaseMerge {
		t.Fatalf("phase filter = %+v, want 1 merge", got)
	}
}

func TestEventsInterventionOnly(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	got := decodeEvents(t, getEvents(t, s, "?intervention=1"))
	if len(got) != 1 || !got[0].InterventionNeeded() {
		t.Fatalf("intervention filter = %+v, want 1 intervention-needed", got)
	}
}

func TestEventsSinceCutoff(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	// since = base+2m → drops the first two events, keeps the last two.
	since := eventsBaseTS.Add(2 * time.Minute).Format(time.RFC3339)
	got := decodeEvents(t, getEvents(t, s, "?since="+since))
	if len(got) != 2 {
		t.Fatalf("since len = %d, want 2", len(got))
	}
}

func TestEventsLimit(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	got := decodeEvents(t, getEvents(t, s, "?limit=2"))
	if len(got) != 2 {
		t.Fatalf("limit len = %d, want 2", len(got))
	}
	// Most-recent window (review F1): the newest two events, ascending — the seed's
	// last two are verify/intervention-needed then merge/merge.
	if got[0].Phase != events.PhaseVerify || got[1].Phase != events.PhaseMerge {
		t.Errorf("limit window = %+v, want most-recent two (verify, merge)", got)
	}
}

func TestEventsEmptyIsArrayNotNull(t *testing.T) {
	s, _ := newEventsServer(t)
	rec := getEvents(t, s, "?project=nope")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("empty events body = %q, want []", body)
	}
}

func TestEventsAuth401WithoutToken(t *testing.T) {
	s, bus := newEventsServer(t)
	seedEventsHistory(t, bus)
	req := httptest.NewRequest(http.MethodGet, "/events", nil)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestEventsBadRequests(t *testing.T) {
	s, _ := newEventsServer(t)
	cases := []struct {
		query string
		msg   string
	}{
		{"?since=not-a-time", "invalid since"},
		{"?limit=abc", "invalid limit"},
		{"?phase=bogus", "invalid phase"},
		{"?kind=bogus", "invalid kind"},
	}
	for _, c := range cases {
		rec := getEvents(t, s, c.query)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", c.query, rec.Code)
			continue
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["error"] != c.msg {
			t.Errorf("%s error = %q, want %q", c.query, body["error"], c.msg)
		}
	}
}

// busOnly implements events.EventBus but NOT events.EventReader, to drive the
// 501 not-a-reader guard in handleEvents.
type busOnly struct{}

func (busOnly) Publish(context.Context, events.Event) error { return nil }
func (busOnly) Subscribe(context.Context, events.Filter) (<-chan events.Event, events.CancelFunc, error) {
	ch := make(chan events.Event)
	return ch, func() {}, nil
}

func TestEventsNotImplementedWhenNoReader(t *testing.T) {
	// asReader on a bus that is not an EventReader yields nil → 501.
	s := &apiServer{
		store:  statestore.NewMemoryStore(),
		bus:    busOnly{},
		reader: asReader(busOnly{}),
		token:  testToken,
		clock:  fixedClock,
	}
	if s.reader != nil {
		t.Fatalf("asReader(busOnly) = %v, want nil", s.reader)
	}
	rec := getEvents(t, s, "")
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "event history not supported" {
		t.Errorf("error = %q", body["error"])
	}
}

// --- GET /ws roundtrip ---

// wsTestServer starts an in-process httptest server over the gateway routes and
// returns it plus the bus the server subscribes to.
func wsTestServer(t *testing.T) (*httptest.Server, *events.MemoryBus) {
	t.Helper()
	s, bus := newEventsServer(t)
	srv := httptest.NewServer(s.routes())
	t.Cleanup(srv.Close)
	return srv, bus
}

// wsURL converts an http(s) base URL to a ws(s) /ws URL with the given query.
func wsURL(base, query string) string {
	u := strings.Replace(base, "http://", "ws://", 1)
	u = strings.Replace(u, "https://", "wss://", 1)
	return u + "/ws" + query
}

// dialWS dials /ws with the given query and optional Authorization header value.
func dialWS(ctx context.Context, t *testing.T, base, query, authHeader string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	var opts *websocket.DialOptions
	if authHeader != "" {
		h := http.Header{}
		h.Set("Authorization", authHeader)
		opts = &websocket.DialOptions{HTTPHeader: h}
	}
	return websocket.Dial(ctx, wsURL(base, query), opts)
}

// publishUntilDelivered publishes ev repeatedly (until ctx done) so the test is
// not sensitive to the exact moment the server's subscription goes live. The
// reader side stops it via cancel once it has received an event.
func publishUntilDelivered(ctx context.Context, bus *events.MemoryBus, ev events.Event) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		_ = bus.Publish(ctx, ev)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func TestWSRoundtripQueryToken(t *testing.T) {
	srv, bus := wsTestServer(t)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()

	conn, _, err := dialWS(dialCtx, t, srv.URL, "?token="+testToken, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "done") })

	pubCtx, cancelPub := context.WithCancel(context.Background())
	defer cancelPub()
	go publishUntilDelivered(pubCtx, bus, events.Event{
		Project: "proj-a", Task: "T-1", Phase: events.PhasePlan, Kind: events.KindStarted,
	})

	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	var got events.Event
	if err := wsjson.Read(readCtx, conn, &got); err != nil {
		t.Fatalf("read: %v", err)
	}
	cancelPub()
	if got.Project != "proj-a" || got.Phase != events.PhasePlan || got.Kind != events.KindStarted {
		t.Errorf("event = %+v, want proj-a plan started", got)
	}
}

func TestWSRoundtripHeaderToken(t *testing.T) {
	srv, bus := wsTestServer(t)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()

	conn, _, err := dialWS(dialCtx, t, srv.URL, "", bearer())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "done") })

	pubCtx, cancelPub := context.WithCancel(context.Background())
	defer cancelPub()
	go publishUntilDelivered(pubCtx, bus, events.Event{
		Project: "proj-x", Task: "T-7", Phase: events.PhaseTest, Kind: events.KindProgress,
	})

	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	var got events.Event
	if err := wsjson.Read(readCtx, conn, &got); err != nil {
		t.Fatalf("read: %v", err)
	}
	cancelPub()
	if got.Project != "proj-x" || got.Kind != events.KindProgress {
		t.Errorf("event = %+v, want proj-x progress", got)
	}
}

func TestWSProjectFilter(t *testing.T) {
	srv, bus := wsTestServer(t)
	dialCtx, cancelDial := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDial()

	conn, _, err := dialWS(dialCtx, t, srv.URL, "?token="+testToken+"&project=proj-keep", "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "done") })

	pubCtx, cancelPub := context.WithCancel(context.Background())
	defer cancelPub()
	// Publish both a non-matching and a matching event each tick; only matching
	// should ever be delivered.
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			_ = bus.Publish(pubCtx, events.Event{
				Project: "proj-drop", Task: "T-1", Phase: events.PhasePlan, Kind: events.KindLog,
			})
			_ = bus.Publish(pubCtx, events.Event{
				Project: "proj-keep", Task: "T-1", Phase: events.PhasePlan, Kind: events.KindStarted,
			})
			select {
			case <-pubCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()

	readCtx, cancelRead := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRead()
	// First delivered event must match the filter.
	var got events.Event
	if err := wsjson.Read(readCtx, conn, &got); err != nil {
		t.Fatalf("read: %v", err)
	}
	cancelPub()
	if got.Project != "proj-keep" {
		t.Errorf("filtered event project = %q, want proj-keep", got.Project)
	}
}

func TestWSRejectsBadToken(t *testing.T) {
	srv, _ := wsTestServer(t)
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cases := []struct {
		name  string
		query string
		auth  string
	}{
		{"no token", "", ""},
		{"wrong query token", "?token=nope", ""},
		{"wrong header token", "", "Bearer nope"},
	}
	for _, c := range cases {
		conn, resp, err := dialWS(dialCtx, t, srv.URL, c.query, c.auth)
		if err == nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
			t.Errorf("%s: dial succeeded, want handshake failure", c.name)
			continue
		}
		if resp == nil {
			t.Errorf("%s: nil response on failed dial", c.name)
			continue
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", c.name, resp.StatusCode)
		}
	}
}
