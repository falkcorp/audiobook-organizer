// file: internal/books/cutover/m4/actor_internal_test.go
// version: 1.0.0
// guid: 1e7f4a29-c065-4b83-a2d9-5b8c3e6f0d17
// last-edited: 2026-11-28

package m4

import (
	"io"
	"log/slog"
	"testing"
)

type countOutcomes map[string]int

func (c countOutcomes) Count(o string) { c[o]++ }

// TestNoteUnmappedMethods: each unmapped actor method is one outcome in the
// report, and a clean import counts none.
func TestNoteUnmappedMethods(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w := countOutcomes{}
	noteUnmappedMethods(w, quiet, "01HBATRUN", nil)
	if len(w) != 0 {
		t.Fatalf("a run with only listed methods counted %v", w)
	}
	noteUnmappedMethods(w, quiet, "01HBATRUN", []string{"oidc_token", "oidc_token", "magic"})
	if w[OutcomeAuthMethodUnmapped] != 3 || len(w) != 1 {
		t.Fatalf("three unmapped actors counted %v, want %s: 3", w, OutcomeAuthMethodUnmapped)
	}
}

// TestConvert_ImportActor: the user id is kept, the method normalised, and
// System is the caller's to set.
func TestConvert_ImportActor(t *testing.T) {
	a, listed := importActor(headActor{UserID: "bruce.wayne", AuthMethod: "api_key"})
	if !listed || a.UserID != "bruce.wayne" || a.AuthMethod != "token" || a.System != "" {
		t.Fatalf("importActor = %+v, %v", a, listed)
	}
	if a, listed = importActor(headActor{UserID: "x", AuthMethod: "weird"}); listed || a.AuthMethod != "weird" {
		t.Fatalf("importActor of an unlisted method = %+v, %v", a, listed)
	}
}
