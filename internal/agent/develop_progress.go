package agent

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Live DEVELOP progress (step 7): surface what the performer's `claude -p` develop is ACTUALLY
// doing — which file it reads/writes, what it searches, when it thinks — so the editor's session
// timeline shows rich activity + a liveness pulse instead of a generic, endlessly-repeated
// "files changed" number. The director is never blind to whether the claude instance is alive.
//
// It NEVER touches the develop subprocess or its verdict. claude already writes a JSONL transcript
// of every session under ~/.claude/projects/<encoded-cwd>/; we simply READ that side-artifact's
// tail and reuse parseEnhanceLine — the transcript carries the SAME assistant/tool_use shapes as
// enhance's stream-json. A missing/locked/oddly-named transcript degrades to "" (the generic pulse
// still fires), so this observability is best-effort and can never fail a run.

// maxTranscriptTail bounds how many bytes we read from the END of the transcript per probe. The
// latest assistant tool_use (what we surface) is always near the end and small, so a bounded tail
// keeps the periodic probe cheap even when the transcript grows to many MB.
const maxTranscriptTail = 256 * 1024

// latestDevelopActivity returns the most recent human-readable activity line (📖/✍️/🔎/⚙️/🤔) for
// the develop running in worktree wsPath, or "" when there is no transcript or no surfaced
// activity yet. Best-effort + read-only.
func latestDevelopActivity(wsPath string) string {
	f := newestTranscript(wsPath)
	if f == "" {
		return ""
	}
	return latestActivityInTranscript(f)
}

// latestActivityInTranscript reads the tail of one transcript file and returns the newest activity
// line it can surface, scanning newest→oldest. A partial first line (from the bounded tail seek)
// simply fails parseEnhanceLine and is skipped, so no special handling is needed.
func latestActivityInTranscript(path string) string {
	tail, err := readTail(path, maxTranscriptTail)
	if err != nil {
		return ""
	}
	lines := bytes.Split(tail, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		if ev, ok := parseEnhanceLine(lines[i]); ok && ev.progress != "" {
			return ev.progress
		}
	}
	return ""
}

// encodeCwd derives claude's transcript-directory name from a working directory: every '/' and '.'
// becomes '-' (verified empirically against a live develop — the worktree
// /Users/everva/.conductor-agent/worktrees/optiway/CPB1 maps to the dir
// -Users-everva--conductor-agent-worktrees-optiway-CPB1).
func encodeCwd(wsPath string) string {
	return strings.NewReplacer("/", "-", ".", "-").Replace(wsPath)
}

// transcriptDir maps a working directory to claude's transcript directory under ~/.claude/projects.
// Returns "" if HOME or wsPath is unknown; the directory may not exist yet (claude creates it on
// the session's first tool use).
func transcriptDir(wsPath string) string {
	wsPath = strings.TrimSpace(wsPath)
	home, err := os.UserHomeDir()
	if err != nil || home == "" || wsPath == "" {
		return ""
	}
	return filepath.Join(home, ".claude", "projects", encodeCwd(wsPath))
}

// newestTranscript returns the path of the most-recently-modified *.jsonl in the worktree's
// transcript dir, or "" if the dir is absent/empty. A develop is one claude session, but a retry or
// resume can leave several files — the newest is the live one.
func newestTranscript(wsPath string) string {
	dir := transcriptDir(wsPath)
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var newest string
	var newestMod int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if m := info.ModTime().UnixNano(); m >= newestMod {
			newestMod = m
			newest = filepath.Join(dir, e.Name())
		}
	}
	return newest
}

// readTail returns up to the last maxBytes of the file at path, seeking from the end so a large
// transcript stays cheap to probe.
func readTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if start := info.Size() - maxBytes; start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}
