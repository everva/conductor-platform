// Package main: S-3 recipe argv trust-boundary tripwire tests. warnIfShellArgv is
// a conservative WARNING (never a hard-fail) when a per-project recipe's develop or
// gate argv looks like a shell invocation (argv[0] is a shell, or argv has "-c") —
// the operator's cue that the -recipe-dir repo's .conductor/config.yaml is TRUSTED,
// host-executed input. These prove the tripwire fires on the injection shapes and
// stays quiet for legitimate tool invocations.
package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func warnCapture(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return logger, &buf
}

func TestWarnIfShellArgv_FiresOnShellShapes(t *testing.T) {
	cases := []struct {
		name string
		argv []string
	}{
		{"sh", []string{"sh", "build.sh"}},
		{"bash", []string{"bash", "x"}},
		{"zsh", []string{"zsh"}},
		{"absolute /bin/sh", []string{"/bin/sh", "-c", "go build ./..."}},
		{"dash-c on a tool", []string{"go", "run", "-c", "x"}}, // contains "-c"
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger, buf := warnCapture(t)
			warnIfShellArgv(logger, "develop", tc.argv)
			if !strings.Contains(buf.String(), "shell invocation") {
				t.Fatalf("argv %v must trip the S-3 shell warning; log:\n%s", tc.argv, buf.String())
			}
		})
	}
}

func TestWarnIfShellArgv_QuietForDirectToolArgv(t *testing.T) {
	cases := [][]string{
		{"go", "build", "./..."},
		{"go", "test", "./..."},
		{"npm", "test"},
		{"pytest", "-q"},
		{"imagediff", "--threshold", "0.1"},
		{"golangci-lint", "run"},
		nil, // empty argv is a no-op
	}
	for _, argv := range cases {
		logger, buf := warnCapture(t)
		warnIfShellArgv(logger, "gate", argv)
		if strings.Contains(buf.String(), "shell invocation") {
			t.Fatalf("legitimate tool argv %v must NOT warn; log:\n%s", argv, buf.String())
		}
	}
}
