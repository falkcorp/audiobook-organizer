// file: internal/books/cutover/rename_table_structure_test.go
// version: 1.0.0
// guid: 4d7b2c10-93aa-4a33-8f0f-6c6a1d2e5b04
// last-edited: 2026-11-11

package cutover

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// validTarget reports whether p is a config path or an integrations path (a
// passthrough key or a secret name, which config does not register).
func validTarget(p string) bool {
	if config.Path(p).Validate() == nil {
		return true
	}
	return strings.HasPrefix(p, "integrations.") && secrets.Name(p).Validate() == nil
}

// topCounts counts the table's top-level rows by class.
func topCounts(t *RenameTable) (top int, byClass map[Class]int, twoTarget int) {
	byClass = map[Class]int{}
	for _, r := range t.Rows {
		if !r.Top {
			continue
		}
		top++
		byClass[r.Class]++
		if len(r.Targets) == 2 {
			twoTarget++
		}
	}
	return top, byClass, twoTarget
}

// TestRenameTable_Counts: the generated table's counts are the spec's
// (05-config §1.12.3): 173 top-level rows, 134 k, 11 s, 9 i, 16 o, 3 x, and
// two rows with two targets (oauth_allowed_emails and maintenance).
func TestRenameTable_Counts(t *testing.T) {
	top, by, two := topCounts(Table())
	if top != 173 {
		t.Errorf("top-level rows = %d, want 173", top)
	}
	for c, want := range map[Class]int{ClassKey: 134, ClassSecret: 11, ClassInput: 9, ClassEnv: 16, ClassDropped: 3} {
		if by[c] != want {
			t.Errorf("class %s rows = %d, want %d", c, by[c], want)
		}
	}
	if two != 2 {
		t.Errorf("rows with two targets = %d, want 2", two)
	}
	for _, h := range []string{"oauth_allowed_emails", "maintenance"} {
		if r, ok := Table().Row(h); !ok || len(r.Targets) != 2 {
			t.Errorf("row %s targets = %+v, want two", h, r)
		}
	}
}

// TestRenameTable_FailClosedRows: the two FailClosed keys are exactly the rows
// the spec marks, so M2 stages them even at their defaults.
func TestRenameTable_FailClosedRows(t *testing.T) {
	var got []string
	for _, r := range Table().Rows {
		if r.FailClosed {
			for _, tg := range r.Targets {
				got = append(got, tg.Path)
			}
		}
	}
	slices.Sort(got)
	if want := []string{"core.auth.mode", "core.http.basic_gate.enabled"}; !slices.Equal(got, want) {
		t.Errorf("FailClosed targets = %v, want %v", got, want)
	}
}

// TestRenameTable_SecretRowsMoveUnderTheirOwnNames: every class-s row targets
// secrets only, every class-k row config keys only; no row is stored in
// plaintext anywhere M2 leaves behind (class s is never a key).
func TestRenameTable_SecretRowsMoveUnderTheirOwnNames(t *testing.T) {
	for _, r := range Table().Rows {
		if r.Removed || r.Process {
			continue
		}
		for _, tg := range r.Targets {
			switch r.Class {
			case ClassSecret:
				if tg.Kind != TargetSecret {
					t.Errorf("secret row %s targets a %s (%s)", r.Head, tg.Kind, tg.Path)
				}
			case ClassKey:
				if tg.Kind == TargetSecret {
					t.Errorf("key row %s targets a secret (%s)", r.Head, tg.Path)
				}
			}
		}
	}
}

// TestRenameTable_RetiredNamesAreValidForConfig: config.NewStore, the one
// consumer of the retired list, accepts it (names unique per layer, canonical
// paths well formed), and every retired name's canonical path validates.
func TestRenameTable_RetiredNamesAreValidForConfig(t *testing.T) {
	retired := RetiredConfigNames()
	if len(retired) == 0 {
		t.Fatal("RetiredConfigNames is empty")
	}
	if _, err := config.NewStore(config.Options{AppID: "abo", Retired: retired}); err != nil {
		t.Fatalf("config.NewStore refuses the retired list: %v", err)
	}
	for _, r := range retired {
		if r.Canonical != "" && !validTarget(string(r.Canonical)) {
			t.Errorf("retired %q: canonical %q is neither a config path nor an integrations path", r.Name, r.Canonical)
		}
	}
}

// TestRenameTable_RetiredEnvNamesNeverCollideWithCanonicalOnes: no retired env
// name is also a canonical name the deploy files may set.
func TestRenameTable_RetiredEnvNamesNeverCollideWithCanonicalOnes(t *testing.T) {
	canon := map[string]bool{}
	for _, n := range CanonicalEnvNames() {
		canon[n] = true
		if n == "" || strings.ContainsAny(n, ". -") && !strings.HasPrefix(n, "OTEL_") {
			t.Errorf("canonical env name %q is not a plain environment name", n)
		}
	}
	for _, r := range RetiredConfigNames() {
		if !r.File && canon[r.Name] {
			t.Errorf("env name %s is both retired and canonical", r.Name)
		}
	}
	if !slices.IsSorted(CanonicalEnvNames()) {
		t.Error("CanonicalEnvNames is not sorted")
	}
}

// TestRenameTable_TargetsAreWellFormed: every key and secret target is a valid
// path (a config path or a secret name), and every row of a class that moves
// has one.
func TestRenameTable_TargetsAreWellFormed(t *testing.T) {
	for _, r := range Table().Rows {
		moves := r.Class == ClassKey || r.Class == ClassSecret
		if moves && !r.Removed && !r.Process && len(r.Targets) == 0 && !r.Container {
			t.Errorf("row %s (class %s) has no target", r.Head, r.Class)
		}
		for _, tg := range r.Targets {
			if tg.Kind != TargetKey && tg.Kind != TargetSecret || !isConcretePath(tg.Path) {
				continue
			}
			var err error
			if tg.Kind == TargetSecret {
				err = secrets.Name(tg.Path).Validate()
			} else {
				err = config.Path(tg.Path).Validate()
			}
			if err != nil {
				t.Errorf("row %s: %s target %s: %v", r.Head, tg.Kind, tg.Path, err)
			}
		}
	}
}
