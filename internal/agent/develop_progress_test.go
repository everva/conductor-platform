package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEncodeCwd pins the cwd→transcript-dir encoding to the EXACT mapping observed from a live
// develop (the worktree path on the left produced the dir name on the right under ~/.claude/projects).
func TestEncodeCwd(t *testing.T) {
	cases := map[string]string{
		"/Users/everva/.conductor-agent/worktrees/optiway/CPB1": "-Users-everva--conductor-agent-worktrees-optiway-CPB1",
		"/home/davinci/.conductor-agent/worktrees/optiway/T-1":  "-home-davinci--conductor-agent-worktrees-optiway-T-1",
		"/a/b.c": "-a-b-c",
		"plain":  "plain",
	}
	for in, want := range cases {
		if got := encodeCwd(in); got != want {
			t.Fatalf("encodeCwd(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLatestActivityInTranscript feeds a realistic JSONL transcript (the same assistant/tool_use
// shapes claude writes) and asserts we surface the NEWEST tool activity — skipping result/system
// lines and a partial truncated head line — so the editor shows what the develop is doing now.
func TestLatestActivityInTranscript(t *testing.T) {
	transcript := "partial-truncated-head-line-no-brace\n" +
		`{"type":"system","subtype":"init","session_id":"x"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/repo/apps/web/src/hooks/use-collection-points.ts"}}]}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"grep -rn useAllEmployees ."}}]}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/repo/apps/web/src/components/data-table/bulk-toolbar.tsx"}}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"result\":\"pass\"}"}` + "\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	got := latestActivityInTranscript(path)
	want := progressWriting + ".../data-table/bulk-toolbar.tsx"
	if got != want {
		t.Fatalf("latestActivityInTranscript = %q, want %q", got, want)
	}
}

// TestLatestActivityInTranscript_NoActivity: a transcript of only system+result lines surfaces "".
func TestLatestActivityInTranscript_NoActivity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := `{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","subtype":"success","result":"x"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := latestActivityInTranscript(path); got != "" {
		t.Fatalf("latestActivityInTranscript = %q, want empty", got)
	}
}

// TestNewestTranscript resolves the encoded dir under a fake HOME and picks the newest .jsonl.
func TestNewestTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := "/tmp/wt/optiway/CPB1"
	dir := filepath.Join(home, ".claude", "projects", encodeCwd(ws))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "old.jsonl")
	newer := filepath.Join(dir, "new.jsonl")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(newer, future, future); err != nil {
		t.Fatal(err)
	}
	if got := newestTranscript(ws); got != newer {
		t.Fatalf("newestTranscript = %q, want %q", got, newer)
	}
	// A worktree with no transcript dir resolves to "".
	if got := newestTranscript("/no/such/worktree/path"); got != "" {
		t.Fatalf("newestTranscript(missing) = %q, want empty", got)
	}
}
