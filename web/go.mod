// This file exists ONLY to isolate the web/ frontend toolchain from the Go
// module at the repo root (ADR-0026: "web/ is fully isolated; go build ./...
// must not see it"). A nested module makes the root module's ./... wildcard skip
// this entire subtree, so a stray Go file shipped inside web/node_modules (e.g.
// flatted/golang) can never affect `go build/vet/test ./...`. There is NO Go
// source here; this is config only and does not touch the root module.
module github.com/everva/conductor-platform/web

go 1.26
