// Command conductor-api is the READ half of the Faz-3 API gateway (ADR-0025,
// task 3A-1): a SEPARATE service from the conductor daemon that exposes a
// frontend-agnostic HTTP/JSON surface over the SHARED, FROZEN
// statestore.StateStore. Browsers and the future editor fork cannot reach
// Postgres or the daemon's internal packages directly; this gateway is the
// stable seam between them.
//
// It is read-only over the store: it consumes ONLY existing StateStore read
// methods (ListProjects/ListTasks/GetProject/ListHosts/ListLeases) and adds
// NOTHING to the frozen contract (ADR-0021: here, pure consumption). The
// conductor daemon (cmd/conductor) is NOT modified — this is its own binary,
// its own process/pod, deployed alongside the daemon.
//
// Store backend selection mirrors the daemon: an empty -dsn / CONDUCTOR_DSN
// selects the in-memory store (dev/test); a non-empty DSN selects the central
// Postgres store (the SAME DSN the daemon uses). A cleanup closure releases the
// Postgres pool on exit and is always non-nil (a no-op for the memory store).
//
// Auth is a single bearer token read from CONDUCTOR_API_TOKEN (env ONLY — never
// a flag, so it does not leak in `ps`). All endpoints except /healthz and
// /readyz require it; the comparison is constant-time. The token and the DSN are
// NEVER logged or echoed in any response (httpserver.go secret-free discipline);
// startup logs only the listen addr and the store backend NAME (memory/postgres).
//
// As a safety default the server REFUSES to start when CONDUCTOR_API_TOKEN is
// empty: an unauthenticated control-plane surface is never run by accident.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
)

// shutdownTimeout bounds the graceful drain on SIGINT/SIGTERM so a stuck
// connection cannot wedge the process during shutdown.
const shutdownTimeout = 10 * time.Second

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx := context.Background()
	os.Exit(run(ctx, os.Args[1:], logger, os.Stderr))
}

// config is the resolved service configuration. The token is intentionally NOT a
// flag (flags leak via `ps`); it comes from the environment only.
type config struct {
	addr  string
	dsn   string
	token string
}

// parseConfig resolves flags (each falling back to an env var) and the
// env-only bearer token. It mirrors the daemon's flagset/envOr pattern.
func parseConfig(argv []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("conductor-api", flag.ContinueOnError)
	fs.SetOutput(stderr)

	addr := fs.String("addr", envOr("CONDUCTOR_API_ADDR", ":8080"),
		"listen address for the API gateway")
	dsn := fs.String("dsn", envOr("CONDUCTOR_DSN", ""),
		"Postgres DSN for the shared statestore (empty = in-memory store, dev/test)")

	if err := fs.Parse(argv); err != nil {
		return config{}, err
	}

	return config{
		addr: *addr,
		dsn:  *dsn,
		// Token is read from the environment ONLY — never a flag — so it does not
		// appear in the process argument list.
		token: os.Getenv("CONDUCTOR_API_TOKEN"),
	}, nil
}

// run wires the service and serves until the context is cancelled (SIGINT/
// SIGTERM). It returns a process exit code: 0 on clean shutdown, 1 on a wiring/
// serve error, 2 on a configuration error.
func run(ctx context.Context, argv []string, logger *slog.Logger, stderr io.Writer) int {
	cfg, err := parseConfig(argv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "conductor-api: %v\n", err)
		return 2
	}

	// Safety default: refuse to run an unauthenticated control plane.
	if cfg.token == "" {
		_, _ = fmt.Fprintln(stderr,
			"conductor-api: CONDUCTOR_API_TOKEN required: refusing to run an unauthenticated control plane")
		return 2
	}
	// Auth hardening (3C-1): refuse a weak or placeholder token at startup so the
	// gateway is never deployed with the secret.yaml placeholder or a trivially
	// guessable secret. The message names the requirement WITHOUT echoing the token.
	if err := validateToken(cfg.token); err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor-api: %v\n", err)
		return 2
	}

	store, closeStore, err := newStore(ctx, cfg, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor-api: %v\n", err)
		return 1
	}
	// Release backend resources (the Postgres pgxpool) on exit. No-op for memory.
	defer closeStore()

	bus, closeBus, err := newBus(ctx, cfg, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor-api: %v\n", err)
		return 1
	}
	// Release the event bus backend (its own pgxpool / subscriptions) on exit.
	defer closeBus()

	api := &apiServer{
		store: store,
		bus:   bus,
		// Same concrete value backs history replay when it implements the additive
		// EventReader seam (both real impls do); nil → GET /events returns 501.
		reader: asReader(bus),
		token:  cfg.token,
		clock:  time.Now,
		// Production distiller: the real `claude -p` subscription path (no API key).
		// It only DRAFTS proposed scenarios for human review at POST /distill; it
		// persists nothing. Tests inject a stub via the apiServer field instead.
		distiller: intake.NewCommandDistiller(),
	}

	// Log ONLY the addr and the backend NAME — never the DSN or the token.
	logger.Info("conductor-api starting",
		slog.String("addr", cfg.addr),
		slog.String("store_backend", storeBackendName(cfg.dsn)))

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Trap SIGINT/SIGTERM: cancelling this context triggers a graceful shutdown.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "conductor-api: %v\n", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_, _ = fmt.Fprintf(stderr, "conductor-api: shutdown: %v\n", err)
			return 1
		}
		return 0
	}
}

// newStore selects and constructs the StateStore backend from cfg.dsn and
// returns it alongside a closer that releases any backend resource (the Postgres
// pool). The closer is always non-nil: for the in-memory store it is a no-op, so
// callers can defer it unconditionally. The active backend is logged WITHOUT the
// DSN (which carries the password) — only the backend name is emitted. This
// mirrors the daemon's newStore (cmd/conductor/main.go).
func newStore(ctx context.Context, cfg config, logger *slog.Logger) (statestore.StateStore, func(), error) {
	if cfg.dsn == "" {
		logger.Info("statestore backend selected", slog.String("backend", "memory"))
		return statestore.NewMemoryStore(), func() {}, nil
	}

	pg, err := statestore.NewPostgresStore(ctx, cfg.dsn)
	if err != nil {
		// pgx does not echo the password in its error; we still never log cfg.dsn.
		return nil, nil, fmt.Errorf("open postgres store: %w", err)
	}
	logger.Info("statestore backend selected", slog.String("backend", "postgres"))
	return pg, pg.Close, nil
}

// newBus selects and constructs the event bus the gateway subscribes to (/ws)
// and replays from (GET /events), mirroring the daemon's newEmitter and the
// store's DSN-driven backend selection: an empty DSN selects the in-memory bus
// (events observable in-process; dev/test); a non-empty DSN selects the Postgres
// LISTEN/NOTIFY bus so the gateway sees events published by the daemon on other
// processes/hosts in realtime. It returns the bus alongside a closer that
// releases the backend resource (the PG bus's own pgxpool / the memory bus's
// subscriptions); the closer is always non-nil so callers can defer it
// unconditionally. The DSN (which carries the password) is NEVER logged — only
// the backend name is emitted.
func newBus(ctx context.Context, cfg config, logger *slog.Logger) (events.EventBus, func(), error) {
	if cfg.dsn == "" {
		bus := events.NewMemoryBus()
		logger.Info("event bus backend selected", slog.String("backend", "memory"))
		return bus, bus.Close, nil
	}

	bus, err := events.NewPostgresBus(ctx, cfg.dsn)
	if err != nil {
		// pgx does not echo the password in its error; we still never log cfg.dsn.
		return nil, nil, fmt.Errorf("open postgres event bus: %w", err)
	}
	logger.Info("event bus backend selected", slog.String("backend", "postgres"))
	return bus, bus.Close, nil
}

// storeBackendName maps the DSN to the human backend name surfaced at startup.
// Empty DSN means the in-memory store; any non-empty DSN means Postgres. It
// returns ONLY the backend name — never the DSN — so logs stay secret-free.
func storeBackendName(dsn string) string {
	if dsn == "" {
		return "memory"
	}
	return "postgres"
}

// minTokenLen is the shortest bearer token the gateway will start with. A short
// token is brute-forceable; 16 chars is a low floor (a real token should be a
// long random secret) that still rejects obvious mistakes.
const minTokenLen = 16

// validateToken rejects a weak or placeholder bearer token at startup (auth
// hardening, 3C-1). It enforces a minimum length and refuses any token that
// still contains the secret.yaml placeholder marker, so the gateway cannot be
// deployed with the template secret or a trivially short key. It NEVER includes
// the token value in its error (no secret leak) — only the reason.
func validateToken(token string) error {
	if strings.Contains(token, "REPLACE_ME") {
		return errors.New("CONDUCTOR_API_TOKEN is the template placeholder: set a real, strong random token")
	}
	if len(token) < minTokenLen {
		return fmt.Errorf("CONDUCTOR_API_TOKEN too short (min %d chars): use a long random token", minTokenLen)
	}
	return nil
}

// envOr returns the value of env var key, or def when it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
