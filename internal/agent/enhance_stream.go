package agent

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Live ENHANCE streaming: parse claude's `--output-format stream-json` NDJSON into human-readable
// TURKISH activity lines (📖 Okunuyor / 🔎 Aranıyor / 🤔 Düşünülüyor) + the final spec. The event
// schema was verified EMPIRICALLY against claude 2.1.185 (docs/ENHANCE-STREAMING-PLAN.md Faz-0):
// `assistant` events carry tool_use/text blocks; the terminal `result` event carries the FULL
// spec in its `result` field (NOT the accumulated stdout). No fabricated lines — every signal
// comes from a real claude event.

const (
	progressThinking = "🤔 Düşünülüyor…"
	progressReading  = "📖 Okunuyor: "
	progressSearch   = "🔎 Aranıyor: "
	progressRunning  = "⚙️ Çalıştırılıyor: "
	progressWriting  = "✍️ Yazılıyor: "
)

// enhanceEvent is the parsed outcome of one stream-json line.
type enhanceEvent struct {
	progress string // non-empty → a live-activity line to surface
	final    bool   // true → the terminal result event
	spec     string // final spec text (final && !failed)
	failed   bool   // the result event reported an error
	errMsg   string // short failure reason (final && failed)
}

// streamLine mirrors only the fields we read from one stream-json event; unknown fields ignored.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// parseEnhanceLine maps one NDJSON line to an enhanceEvent. ok=false for lines that carry no
// signal (system/init, rate_limit_event, user/tool_result, blank, non-JSON).
func parseEnhanceLine(line []byte) (enhanceEvent, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return enhanceEvent{}, false
	}
	var ev streamLine
	if err := json.Unmarshal(line, &ev); err != nil {
		return enhanceEvent{}, false
	}
	switch ev.Type {
	case "result":
		if ev.IsError || (ev.Subtype != "" && ev.Subtype != "success") {
			msg := strings.TrimSpace(ev.Result)
			if msg == "" {
				msg = "claude reported an error result"
			}
			return enhanceEvent{final: true, failed: true, errMsg: msg}, true
		}
		return enhanceEvent{final: true, spec: strings.TrimSpace(ev.Result)}, true
	case "system":
		if ev.Subtype == "thinking_tokens" {
			return enhanceEvent{progress: progressThinking}, true
		}
		return enhanceEvent{}, false
	case "assistant":
		for _, b := range ev.Message.Content {
			switch b.Type {
			case "tool_use":
				if d := toolProgress(b.Name, b.Input); d != "" {
					return enhanceEvent{progress: d}, true
				}
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					return enhanceEvent{progress: progressThinking}, true
				}
			}
		}
		return enhanceEvent{}, false
	}
	return enhanceEvent{}, false
}

// toolProgress maps a tool_use block to a Turkish activity line, or "" when there is nothing
// worth surfacing. Input key names were verified empirically (Faz-0): Read→file_path,
// Grep/Glob→pattern, Bash→command.
func toolProgress(name string, input json.RawMessage) string {
	var in struct {
		Pattern  string `json:"pattern"`
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
		Path     string `json:"path"`
	}
	_ = json.Unmarshal(input, &in)
	switch name {
	case "Read", "NotebookRead":
		return progressReading + shortPath(in.FilePath)
	case "Grep", "Glob":
		p := strings.TrimSpace(in.Pattern)
		if p == "" {
			p = shortPath(in.Path)
		}
		return progressSearch + p
	case "Bash":
		c := strings.TrimSpace(in.Command)
		if c == "" {
			return ""
		}
		if isSearchCmd(c) {
			return progressSearch + shortCmd(c)
		}
		return progressRunning + shortCmd(c)
	case "Edit", "MultiEdit", "Write", "NotebookEdit":
		// Read-only enhance should never write, but surface it honestly if it ever does.
		return progressWriting + shortPath(in.FilePath)
	}
	return ""
}

// isSearchCmd is true when a bash command is a code search. The enhance prompt tells claude to
// grep/ripgrep, which it often runs via Bash rather than the native Grep tool.
func isSearchCmd(cmd string) bool {
	head := cmd
	if i := strings.IndexByte(head, ' '); i > 0 {
		head = head[:i]
	}
	if i := strings.LastIndexByte(head, '/'); i >= 0 {
		head = head[i+1:]
	}
	switch head {
	case "grep", "rg", "ripgrep", "ag", "ack", "find", "fd":
		return true
	}
	return false
}

// shortPath trims a long/absolute path to its last two segments (…/dir/file) so a progress line
// stays readable. Empty → "(dosya)".
func shortPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "(dosya)"
	}
	p = strings.Trim(p, "/")
	segs := strings.Split(p, "/")
	if len(segs) <= 2 {
		return p
	}
	return ".../" + segs[len(segs)-2] + "/" + segs[len(segs)-1]
}

// shortCmd collapses whitespace and rune-truncates a command for a compact progress label.
func shortCmd(c string) string {
	c = strings.Join(strings.Fields(c), " ")
	const max = 48
	r := []rune(c)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return c
}
