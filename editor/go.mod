// This file exists ONLY to isolate the editor/ extension toolchain from the Go
// module at the repo root (ADR-0027: the VS Code extension lives in editor/ and
// must not be seen by `go build ./...`). A nested module makes the root module's
// ./... wildcard skip this entire subtree, so a stray Go file shipped inside
// editor/node_modules (e.g. flatted/golang) can never affect `go build/vet/test
// ./...`. There is NO Go source here; this is config only and does not touch the
// root module.
module github.com/everva/conductor-platform/editor

go 1.26
