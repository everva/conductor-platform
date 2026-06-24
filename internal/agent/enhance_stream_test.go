package agent

import "testing"

// The fixtures below mirror the EXACT stream-json shapes observed from claude 2.1.185
// (docs/ENHANCE-STREAMING-PLAN.md Faz-0): assistant events carry tool_use/text blocks with
// input keys file_path (Read) / pattern (Grep,Glob) / command (Bash); the terminal result
// event carries the full spec in .result.
func TestParseEnhanceLine(t *testing.T) {
	cases := []struct {
		name   string
		line   string
		wantOK bool
		want   enhanceEvent
	}{
		{"system init ignored", `{"type":"system","subtype":"init","session_id":"x"}`, false, enhanceEvent{}},
		{"rate limit ignored", `{"type":"rate_limit_event","tier":"x"}`, false, enhanceEvent{}},
		{"tool_result ignored", `{"type":"user","message":{"content":[{"type":"tool_result","content":"…"}]}}`, false, enhanceEvent{}},
		{"blank ignored", ``, false, enhanceEvent{}},
		{"non-json ignored", `not json`, false, enhanceEvent{}},

		{"thinking_tokens", `{"type":"system","subtype":"thinking_tokens"}`, true, enhanceEvent{progress: progressThinking}},

		{
			"read tool → short path",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/repo/apps/api/src/service-company/service-company.service.ts"}}]}}`,
			true, enhanceEvent{progress: progressReading + ".../service-company/service-company.service.ts"},
		},
		{
			"grep tool → pattern",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"output_mode":"files_with_matches","pattern":"serviceCompanyId"}}]}}`,
			true, enhanceEvent{progress: progressSearch + "serviceCompanyId"},
		},
		{
			"glob tool → pattern",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Glob","input":{"pattern":"**/*.spec.ts"}}]}}`,
			true, enhanceEvent{progress: progressSearch + "**/*.spec.ts"},
		},
		{
			"bash grep → search",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"grep -rl \"hello-conductor\" ."}}]}}`,
			true, enhanceEvent{progress: progressSearch + `grep -rl "hello-conductor" .`},
		},
		{
			"bash other → run",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`,
			true, enhanceEvent{progress: progressRunning + "ls -la"},
		},
		{
			"assistant text → thinking",
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check the schema."}]}}`,
			true, enhanceEvent{progress: progressThinking},
		},
		{
			"assistant empty text → ignored",
			`{"type":"assistant","message":{"content":[{"type":"text","text":"   "}]}}`,
			false, enhanceEvent{},
		},

		{
			"result success → final spec",
			`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"## Türkçe spec\n- ...","duration_ms":9214}`,
			true, enhanceEvent{final: true, spec: "## Türkçe spec\n- ..."},
		},
		{
			"result error → final failed",
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`,
			true, enhanceEvent{final: true, failed: true, errMsg: "boom"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseEnhanceLine([]byte(tc.line))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (event=%+v)", ok, tc.wantOK, got)
			}
			if got != tc.want {
				t.Fatalf("event = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A long, absolute path collapses to its last two segments; a short path is kept (sans leading /).
func TestShortPath(t *testing.T) {
	cases := map[string]string{
		"/a/b/c/d.ts":  ".../c/d.ts",
		"/only/two.ts": "only/two.ts",
		"single.ts":    "single.ts",
		"":             "(dosya)",
	}
	for in, want := range cases {
		if got := shortPath(in); got != want {
			t.Fatalf("shortPath(%q) = %q, want %q", in, got, want)
		}
	}
}
