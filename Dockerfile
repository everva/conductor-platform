# syntax=docker/dockerfile:1
#
# Multi-stage image for the conductor-platform daemon (P4-1).
#
# Stage 1 (builder) compiles three STATIC binaries from this module:
#   - conductor     the tick daemon (cmd/conductor)        — the ENTRYPOINT
#   - conductorctl  the operator CLI (cmd/conductorctl)     — also on PATH
#   - conductor-api the API gateway (cmd/conductor-api)     — also on PATH; the
#     gateway Deployment overrides `command` to run it (Faz-3, ADR-0025). It is a
#     SEPARATE service (read+control over the shared store/bus); the daemon image
#     is unchanged — same image, different entrypoint per Deployment.
# Stage 2 (runtime) is a debian node-slim base carrying what the services need at
# runtime: `git` (the provisioner shells out to clone + worktree), `ca-certificates`
# (HTTPS to the git remote / API), and the `claude` CLI (the gateway's intake distiller
# shells out to it for POST /distill — Faz-R, ADR-0053; subscription auth at runtime,
# never baked in). It runs as a NON-ROOT user. (Was alpine; moved to glibc node-slim so
# the npm-distributed claude CLI runs on its most-compatible base.)
#
# NO SECRET IS BAKED IN. The image is configured entirely by env at runtime:
#   CONDUCTOR_DSN          Postgres connection string (empty = in-memory dev store)
#   CONDUCTOR_PROJECT      project id to tick (REQUIRED to run the daemon)
#   CONDUCTOR_ROOT         workspace root for clones/worktrees (defaults to /workspace)
#   CONDUCTOR_BASE_BRANCH  integration base branch (default: develop)
#   CONDUCTOR_DEVELOP_CMD  performer argv, e.g. "claude -p" (subscription, NO key)
#   CONDUCTOR_INTERVAL     gap between ticks (default 30s)
#   CONDUCTOR_GOVERNANCE   risk-layered merge policy on/off (default true)
#   CONDUCTOR_HEARTBEAT    liveness heartbeat path (optional)
#   ...plus the rest of the flags in cmd/conductor/main.go (each has an env fallback)
#
# The performer command (`claude -p`) and the gh-token used by git's credential
# helper are supplied at RUNTIME (env / mount). They are NEVER part of the image.

# ---- builder ----------------------------------------------------------------
# golang:1.26 matches go.mod (go 1.26.2). alpine variant keeps the builder small.
FROM golang:1.26-alpine AS builder

# git is occasionally needed for module fetches; build-base is NOT installed
# because we compile with CGO disabled (pure-Go static binaries).
RUN apk add --no-cache git

WORKDIR /src

# Cache the module graph first so dependency downloads are reused across builds
# as long as go.mod/go.sum are unchanged.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Then the source.
COPY . .

# Static, stripped binaries. CGO off => no libc dependency => runs on a minimal
# base. -trimpath drops local paths; -s -w strips the symbol/DWARF tables.
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags='-s -w' -o /out/conductor     ./cmd/conductor && \
    go build -trimpath -ldflags='-s -w' -o /out/conductorctl  ./cmd/conductorctl && \
    go build -trimpath -ldflags='-s -w' -o /out/conductor-api ./cmd/conductor-api

# ---- runtime ----------------------------------------------------------------
# node:22-slim (debian bookworm + Node 22, GLIBC). The gateway's intake distiller (Faz-R,
# ADR-0053) shells out to the `claude` CLI — an npm package — for POST /distill; glibc is the most
# compatible base for it. The SAME image still runs the daemon (different entrypoint); Node/claude
# are inert for the daemon. Subscription auth (CLAUDE_CODE_OAUTH_TOKEN) is supplied at RUNTIME (the
# gateway injects it per /distill from its sealed credential store) — NEVER baked into the image.
FROM node:22-slim AS runtime

# git: the provisioner clones + cuts worktrees by shelling out to git.
# ca-certificates: TLS trust for HTTPS git remotes / APIs.
# tini: a tiny init so the entrypoint (PID 1) reaps children and forwards SIGTERM.
# @anthropic-ai/claude-code: the `claude` CLI the gateway distiller invokes (Faz-R).
RUN apt-get update && \
    apt-get install -y --no-install-recommends git ca-certificates tini && \
    # debian installs tini at /usr/bin/tini; the api Deployment's command hardcodes /sbin/tini
    # (the old alpine path), so symlink it there too — both paths resolve, no chart change needed.
    ln -sf /usr/bin/tini /sbin/tini && \
    npm install -g @anthropic-ai/claude-code && \
    npm cache clean --force && \
    rm -rf /var/lib/apt/lists/* && \
    # Non-root runtime user + a writable HOME (claude writes config/cache there) + workspace.
    groupadd -g 65532 conductor && \
    useradd -u 65532 -g conductor -M -s /usr/sbin/nologin -d /home/conductor conductor && \
    mkdir -p /home/conductor /workspace && \
    chown -R conductor:conductor /home/conductor /workspace

COPY --from=builder /out/conductor     /usr/local/bin/conductor
COPY --from=builder /out/conductorctl  /usr/local/bin/conductorctl
COPY --from=builder /out/conductor-api /usr/local/bin/conductor-api

USER conductor
WORKDIR /workspace

# Default workspace root so the daemon clones under the writable volume/dir. HOME is explicit so
# the `claude` CLI (gateway distiller) has a writable config/cache dir.
ENV CONDUCTOR_ROOT=/workspace \
    HOME=/home/conductor

# Optional health/observability HTTP server (P4-2): /healthz /readyz /status.
# It is DISABLED unless CONDUCTOR_HTTP_ADDR is set (e.g. :8080), so the image's
# default behavior is unchanged. We document the port; compose enables + probes it.
EXPOSE 8080

# tini as PID 1 -> signal-correct graceful shutdown (the daemon traps SIGTERM). On debian tini
# installs to /usr/bin/tini (alpine used /sbin/tini).
ENTRYPOINT ["/usr/bin/tini", "--", "conductor"]

# No project is baked in; the operator MUST supply -project / CONDUCTOR_PROJECT.
# -h prints usage if run with no config, which is a safe, non-erroring default
# for `docker run conductor-platform:test` smoke checks.
CMD ["-h"]
