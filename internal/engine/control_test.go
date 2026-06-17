package engine

import "testing"

// TestControlVocabulary proves the typed control constructors emit the frozen
// Command Action values and that ValidAction recognizes exactly the three verbs.
func TestControlVocabulary(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"pause", PauseCommand(), ActionPause},
		{"resume", ResumeCommand(), ActionResume},
		{"abort", AbortCommand(), ActionAbort},
	}
	for _, tc := range cases {
		if tc.cmd.Action != tc.want {
			t.Errorf("%s: Action = %q, want %q", tc.name, tc.cmd.Action, tc.want)
		}
		if !ValidAction(tc.cmd.Action) {
			t.Errorf("%s: ValidAction(%q) = false, want true", tc.name, tc.cmd.Action)
		}
	}
	if ValidAction("bogus") {
		t.Errorf("ValidAction(bogus) = true, want false")
	}
}

// TestCommandEngine_Control_UsesVocabulary proves Control accepts the typed
// commands and rejects an unknown action (the frozen Control contract path).
func TestCommandEngine_Control_UsesVocabulary(t *testing.T) {
	e := NewCommandEngine(RecipeConfig{DevelopCmd: []string{"true"}})
	for _, cmd := range []Command{PauseCommand(), ResumeCommand(), AbortCommand()} {
		if err := e.Control(t.Context(), cmd); err != nil {
			t.Fatalf("Control(%q): %v", cmd.Action, err)
		}
	}
	if err := e.Control(t.Context(), Command{Action: "bogus"}); err == nil {
		t.Fatalf("Control(bogus) must error")
	}
}
