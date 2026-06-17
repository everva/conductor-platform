package events

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
)

func TestPhaseValid(t *testing.T) {
	for _, p := range Phases {
		if !p.Valid() {
			t.Errorf("phase %q should be valid", p)
		}
	}
	if Phase("bogus").Valid() {
		t.Error("bogus phase should be invalid")
	}
	if Phase("").Valid() {
		t.Error("empty phase should be invalid")
	}
	if len(Phases) != 6 {
		t.Errorf("ADR-0011 froze 6 phases, got %d", len(Phases))
	}
}

func TestKindValid(t *testing.T) {
	for _, k := range Kinds {
		if !k.Valid() {
			t.Errorf("kind %q should be valid", k)
		}
	}
	if Kind("bogus").Valid() {
		t.Error("bogus kind should be invalid")
	}
	if len(Kinds) != 9 {
		t.Errorf("ADR-0011 froze 9 kinds, got %d", len(Kinds))
	}
}

func TestInterventionNeeded(t *testing.T) {
	if !(Event{Kind: KindInterventionNeeded}).InterventionNeeded() {
		t.Error("intervention-needed event should report InterventionNeeded")
	}
	if (Event{Kind: KindLog}).InterventionNeeded() {
		t.Error("log event should not report InterventionNeeded")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		ev   Event
		ok   bool
	}{
		{"good", Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog}, true},
		{"empty project", Event{Phase: PhaseDevelop, Kind: KindLog}, false},
		{"bad phase", Event{Project: "p", Phase: "x", Kind: KindLog}, false},
		{"bad kind", Event{Project: "p", Phase: PhaseDevelop, Kind: "x"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ev.Validate()
			if c.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !c.ok {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errors.Is(err, ErrInvalidEvent) {
					t.Fatalf("expected ErrInvalidEvent, got %v", err)
				}
			}
		})
	}
}

func TestMarshalNilPayloadIsEmptyObject(t *testing.T) {
	b, err := json.Marshal(Event{ID: "i", Project: "p", Phase: PhaseMerge, Kind: KindMerge})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	pl, ok := m["payload"]
	if !ok {
		t.Fatal("payload key missing")
	}
	if _, ok := pl.(map[string]any); !ok {
		t.Fatalf("payload should be an object, got %T", pl)
	}
}

func TestFromEngine(t *testing.T) {
	ee := engine.Event{
		Project: "proj",
		Task:    "T-1",
		Phase:   "develop",
		Kind:    "log",
		Payload: map[string]any{"line": "hello"},
	}
	got := FromEngine(ee)
	if got.Project != "proj" || got.Task != "T-1" {
		t.Fatalf("project/task not copied: %+v", got)
	}
	if got.Phase != PhaseDevelop || got.Kind != KindLog {
		t.Fatalf("phase/kind not mapped: %+v", got)
	}
	if got.Payload["line"] != "hello" {
		t.Fatalf("payload not copied: %+v", got.Payload)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("mapped event should validate: %v", err)
	}
}
