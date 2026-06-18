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
# Stage 2 (runtime) is a small alpine that carries ONLY what the daemon needs at
# runtime: `git` (the provisioner shells out to clone + worktree) and
# `ca-certificates` (HTTPS to the git remote / API). It runs as a NON-ROOT user.
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
FROM alpine:3.21

# git: the provisioner clones + cuts worktrees by shelling out to git.
# ca-certificates: TLS trust for HTTPS git remotes / APIs.
# tini: a tiny init so the daemon (PID 1) reaps children and forwards SIGTERM.
RUN apk add --no-cache git ca-certificates tini && \
    # Non-root runtime user + a writable workspace it owns (CONDUCTOR_ROOT default).
    addgroup -g 65532 -S conductor && \
    adduser  -u 65532 -S conductor -G conductor -h /home/conductor && \
    mkdir -p /workspace && chown -R conductor:conductor /workspace

COPY --from=builder /out/conductor     /usr/local/bin/conductor
COPY --from=builder /out/conductorctl  /usr/local/bin/conductorctl
COPY --from=builder /out/conductor-api /usr/local/bin/conductor-api

USER conductor
WORKDIR /workspace

# Default workspace root so the daemon clones under the writable volume/dir.
ENV CONDUCTOR_ROOT=/workspace

# Optional health/observability HTTP server (P4-2): /healthz /readyz /status.
# It is DISABLED unless CONDUCTOR_HTTP_ADDR is set (e.g. :8080), so the image's
# default behavior is unchanged. We document the port; compose enables + probes it.
EXPOSE 8080

# tini as PID 1 -> signal-correct graceful shutdown (the daemon traps SIGTERM).
ENTRYPOINT ["/sbin/tini", "--", "conductor"]

# No project is baked in; the operator MUST supply -project / CONDUCTOR_PROJECT.
# -h prints usage if run with no config, which is a safe, non-erroring default
# for `docker run conductor-platform:test` smoke checks.
CMD ["-h"]
