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

	"github.com/everva/conductor-platform/internal/credstore"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/holdout"
	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/jackc/pgx/v5/pgxpool"
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
	// credentialKey is the base64 32-byte AES-256 master key for the L3 credential store
	// (ADR-0049). Env-only (like the token) — sourced from a k8s secret. Empty = the
	// credential endpoints are DISABLED (fail-closed); the gateway never stores plaintext.
	credentialKey string
	// distillModel pins the model the `claude -p` intake distiller runs on (Faz-R / k8s):
	// `claude -p --model <distillModel>`. Defaults to claude-opus-4-8 (the user's choice). An
	// operator can override via CONDUCTOR_DISTILL_MODEL; "" disables the flag (subscription default).
	distillModel string
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
		// Credential master key (L3) — env-only secret, like the token.
		credentialKey: os.Getenv("CONDUCTOR_CREDENTIAL_KEY"),
		// Intake distiller model (Faz-R) — default claude-opus-4-8; override via env.
		distillModel: envOr("CONDUCTOR_DISTILL_MODEL", "claude-opus-4-8"),
	}, nil
}

// buildSealer turns the configured base64 master key into a credstore.Sealer (L3, ADR-0049).
// An EMPTY key returns (nil, nil) — the credential store is intentionally disabled and the
// endpoints fail closed. A NON-EMPTY but malformed/wrong-length key is a hard error: an
// operator who set the key meant to enable encryption, so we refuse to start rather than
// silently run with the feature off. The key bytes are never logged.
func buildSealer(base64Key string) (*credstore.Sealer, error) {
	key, err := credstore.KeyFromBase64(base64Key)
	if errors.Is(err, credstore.ErrNotConfigured) {
		return nil, nil // disabled — fail-closed at the endpoints.
	}
	if err != nil {
		return nil, fmt.Errorf("CONDUCTOR_CREDENTIAL_KEY invalid: %w", err)
	}
	return credstore.New(key)
}

// claudeCredentialKind is the credential-store key for the subscription claude OAuth token — the
// SAME kind the editor pushes (PUT /agent/credentials/{kind}) and the agent fetches. Faz-R's
// distiller reuses it so the gateway-side `/distill` and the host agent share ONE credential.
const claudeCredentialKind = "CLAUDE_CODE_OAUTH_TOKEN"

// claudeTokenProvider returns a PER-CALL resolver for the distiller's subscription token (Faz-R,
// Option 3): it fetches the SEALED claude credential and decrypts it via the sealer — the exact
// path handleAgentGetCredential serves to the agent. It returns nil when the store has no
// credential persistence OR no master key is configured; the distiller then inherits the env's own
// subscription auth (local davinci dev), unchanged. The plaintext token is returned to the
// distiller, which places it into ONLY the `claude -p` subprocess env (never os.Setenv).
func claudeTokenProvider(store statestore.StateStore, sealer *credstore.Sealer) func(context.Context) (string, error) {
	cs, ok := store.(statestore.CredentialStore)
	if !ok || sealer == nil {
		return nil
	}
	return func(ctx context.Context) (string, error) {
		c, err := cs.GetCredential(ctx, claudeCredentialKind)
		if err != nil {
			return "", err
		}
		token, err := sealer.Open(c.Ciphertext, c.Nonce)
		if err != nil {
			return "", err
		}
		return string(token), nil
	}
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

	// L3 (ADR-0049): build the credential sealer from the master key, if configured. No key →
	// nil sealer → the credential endpoints fail closed (503). A configured-but-invalid key is
	// a hard startup error (never silently disable encryption when an operator intended it on).
	sealer, err := buildSealer(cfg.credentialKey)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor-api: %v\n", err)
		return 1
	}

	// Faz-R (k8s distiller): wire the `claude -p` distiller to the gateway's SEALED claude
	// credential so `/distill` works in a pod with no ambient claude login. The provider is
	// called PER /distill: it fetches + decrypts CLAUDE_CODE_OAUTH_TOKEN (the same credential the
	// editor pushes / the agent fetches) and the distiller injects it into ONLY the subprocess env
	// (Option 3 — never the gateway's long-lived env). When the store/sealer is absent (local
	// davinci dev) the provider is nil → the distiller inherits the env's own subscription auth
	// (frozen behavior). The model is pinned from config (default claude-opus-4-8).
	distiller := intake.NewCommandDistillerWithClaude(intake.ClaudeDistillerConfig{
		TokenProvider: claudeTokenProvider(store, sealer),
		Model:         cfg.distillModel,
	})

	// Faz-S holdout store: a pg:// store over its OWN pool from the same DSN (the statestore does not
	// expose its pool). Enables PUT /holdouts + GET /agent/holdout. No DSN (memory dev) → nil → both
	// endpoints 501. The pool is closed on exit.
	var holdouts holdoutStore
	if cfg.dsn != "" {
		hpool, herr := pgxpool.New(ctx, cfg.dsn)
		if herr != nil {
			_, _ = fmt.Fprintf(stderr, "conductor-api: holdout pool: %v\n", herr)
			return 1
		}
		defer hpool.Close()
		hs, herr := holdout.NewPG(hpool)
		if herr != nil {
			_, _ = fmt.Fprintf(stderr, "conductor-api: holdout store: %v\n", herr)
			return 1
		}
		holdouts = hs
	}

	api := &apiServer{
		store: store,
		bus:   bus,
		// Same concrete value backs history replay when it implements the additive
		// EventReader seam (both real impls do); nil → GET /events returns 501.
		reader: asReader(bus),
		token:  cfg.token,
		clock:  time.Now,
		// Logger for server-side 500-cause logging (secret-free); see apiServer.logger.
		logger: logger,
		// Production distiller: the real `claude -p` subscription path (no API key).
		// It only DRAFTS proposed scenarios for human review at POST /distill; it
		// persists nothing. Tests inject a stub via the apiServer field instead.
		distiller: distiller,
		sealer:    sealer,
		holdouts:  holdouts,
	}

	// Log ONLY the addr and the backend NAME — never the DSN or the token. Also log WHETHER the
	// credential store is enabled (a boolean — never the key) so operators can confirm L3 config.
	logger.Info("conductor-api starting",
		slog.String("addr", cfg.addr),
		slog.String("store_backend", storeBackendName(cfg.dsn)),
		slog.Bool("credential_store_enabled", sealer != nil))

	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           api.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Trap SIGINT/SIGTERM: cancelling this context triggers a graceful shutdown.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// Tie live /ws connections to the server lifetime so a shutdown signal closes
	// them promptly (review F3). Set before serving; no request is handled until
	// ListenAndServe (started below) is up.
	api.baseCtx = ctx

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
	// Ensure the schema exists before serving (idempotent goose Up). Without this, a
	// gateway pointed at a fresh/unmigrated DB — or started before the daemon — would
	// accept requests and then fail EVERY store op with a 500 ("relation ... does not
	// exist"). The daemon migrates identically on startup (cmd/conductor/main.go);
	// goose is idempotent, so both services running it is safe.
	if err := pg.Migrate(ctx); err != nil {
		pg.Close()
		// goose/pgx errors do not echo the password; we still never log cfg.dsn.
		return nil, nil, fmt.Errorf("migrate postgres store: %w", err)
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
