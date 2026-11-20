// file: core/storage/spec_derived_test.go
// version: 1.0.0
// guid: 5e8b0d3a-7c41-4f29-b6a2-d19e3c70f485
// last-edited: 2026-11-19

package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
)

// TestSpecProblems_Derived: Spec.Derived is checked at Open with the rest of
// the registration rules, so a declaration no index can be built from, a name
// declared twice, or a declaration on a core subsystem (DB.Derived never issues
// a handle to one) is a SpecError naming the owner, not a late failure when the
// module first asks for the handle.
func TestSpecProblems_Derived(t *testing.T) {
	keyspace := func(d ...DerivedIndex) Spec { return Spec{Keyspace: true, Derived: d} }
	for _, c := range []struct {
		name  string
		owner Owner
		spec  Spec
		want  string // a substring of the problem; "" = the spec boots
	}{
		{"valid", "gotham", keyspace(DerivedIndex{Name: "signal", Version: "1"}, DerivedIndex{Name: "cave", Version: "2", Blocking: true}), ""},
		{"none", "gotham", keyspace(), ""},
		{"bad name", "gotham", keyspace(DerivedIndex{Name: "Bat Signal", Version: "1"}), "derived index name"},
		{"empty name", "gotham", keyspace(DerivedIndex{Name: "", Version: "1"}), "derived index name"},
		{"no version", "gotham", keyspace(DerivedIndex{Name: "signal", Version: " "}), `derived index "signal" has no Version`},
		{"duplicate", "gotham", keyspace(DerivedIndex{Name: "signal", Version: "1"}, DerivedIndex{Name: "signal", Version: "2"}), `derived index "signal" is declared twice`},
		{"core owner", "core/search", keyspace(DerivedIndex{Name: "signal", Version: "1"}), "a core subsystem declares no derived indexes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnvFS(t, vfs.NewMem(), OwnerSpec{Owner: c.owner, Enabled: true, Spec: c.spec})
			_, err := e.tryOpen()
			if c.want == "" {
				if err != nil {
					t.Fatalf("a valid Derived spec = %v, want it to boot", err)
				}
				return
			}
			se, ok := errors.AsType[*SpecError](err)
			if !ok {
				t.Fatalf("boot = %v, want a *SpecError", err)
			}
			if !strings.Contains(strings.Join(se.Problems, "\n"), c.want) {
				t.Errorf("problems = %q, want one containing %q", se.Problems, c.want)
			}
			if !strings.Contains(strings.Join(se.Problems, "\n"), string(c.owner)+": ") {
				t.Errorf("problems = %q, want them to name owner %s", se.Problems, c.owner)
			}
		})
	}
}
