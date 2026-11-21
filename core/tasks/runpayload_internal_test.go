// file: core/tasks/runpayload_internal_test.go
// version: 0.1.0
// guid: 5a8e2c71-3f94-4d06-b7a2-9c1e6d0f4b38
// last-edited: 2026-11-20

package tasks

import (
	"reflect"
	"testing"
	"time"
)

// fullRunView is a RunView with every field set to a non-zero value, the
// fixture the round-trip tests read.
func fullRunView() RunView {
	created := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return RunView{
		ID: "run-nightwing-1", Kind: "batcave.sweep", Mode: ModeLive, State: StateFailed, Held: true,
		Reason: "the_court_of_owls", Actor: Actor{UserID: "dick", AuthMethod: "session", System: "core.scheduler"},
		Trigger: TriggerManual, Parent: "run-nightwing-0", PlanRef: "run-nightwing-plan",
		Created: created, Started: created.Add(time.Minute), Finished: created.Add(time.Hour),
		Done: 3, Total: 9, Current: "Talon's case file",
	}
}

// zeroFields lists the fields of v (a struct) that hold their zero value,
// descending into nested structs, by dotted name.
func zeroFields(v reflect.Value, prefix string) []string {
	var out []string
	for i := range v.NumField() {
		f, name := v.Field(i), prefix+v.Type().Field(i).Name
		if f.Kind() == reflect.Struct && f.Type() != reflect.TypeFor[time.Time]() {
			out = append(out, zeroFields(f, name+".")...)
			continue
		}
		if f.IsZero() {
			out = append(out, name)
		}
	}
	return out
}

// TestFullRunViewSetsEveryField guards the fixture: a field added to RunView or
// RunPayload later must be set here, or the round-trip tests below would not
// see whether it survives the redaction's round trip.
func TestFullRunViewSetsEveryField(t *testing.T) {
	if z := zeroFields(reflect.ValueOf(fullRunView()), ""); len(z) != 0 {
		t.Errorf("fullRunView leaves %v zero", z)
	}
	if z := zeroFields(reflect.ValueOf(NewRunPayload(fullRunView())), ""); len(z) != 0 {
		t.Errorf("its payload leaves %v zero", z)
	}
}

// TestRedactPayload_RoutesEveryFieldThroughTheRule: RedactRunPayload's round
// trip carries every field through the rule. A rule that blanks the whole view
// blanks every field of the payload (the old implementation passed only
// Current through RedactRunView, so a rule that blanked anything else, such
// as error text, would have blanked it on REST and not on the stream); a rule
// that changes nothing gives back the payload unchanged, so the round trip
// itself loses nothing.
func TestRedactPayload_RoutesEveryFieldThroughTheRule(t *testing.T) {
	p := NewRunPayload(fullRunView())
	if got, want := redactPayloadWith(p, func(RunView) RunView { return RunView{} }), NewRunPayload(RunView{}); !reflect.DeepEqual(got, want) {
		t.Errorf("a rule that blanks everything left\n%+v\nwant\n%+v", got, want)
	}
	if got := redactPayloadWith(p, func(v RunView) RunView { return v }); !reflect.DeepEqual(got, p) {
		t.Errorf("the identity rule changed the payload:\n%+v\nwant\n%+v", got, p)
	}
}

// TestRunPayloadModeWordsRoundTrip: every mode NewRunPayload writes reads back
// as itself, and a word it never writes reads as unset (it cannot carry text
// the rule did not see through the redaction).
func TestRunPayloadModeWordsRoundTrip(t *testing.T) {
	for _, m := range []Mode{ModePreview, ModeLive, 0} {
		if got := modeOf(m.String()); got != m {
			t.Errorf("modeOf(%q) = %v, want %v", m.String(), got, m)
		}
	}
	p := NewRunPayload(fullRunView())
	p.Mode = "joker-was-here"
	if got := RedactRunPayload(nil, p).Mode; got != "unset" {
		t.Errorf("an unknown mode word left as %q, want unset", got)
	}
}
