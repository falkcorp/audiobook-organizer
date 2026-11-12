// file: internal/books/cutover/rename_table_structure_test.go
// version: 1.1.0
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

// TestRenameTable_FlatViperKeysAreRetired: configFromViper reads eight flat keys
// that head no row of the table: the four per-source base URLs (backfilling
// metadata_sources[].base_url) and the four pre-Wave-4 flat iTunes path keys
// (backfilling the nested library paths). A config.yaml line or an environment
// variable under one of them was silently ignored by the new build, because
// the retired list held only row names. Each now retires as a file key (and
// the iTunes ones as an env name too: the base URLs' env names come from their
// BindEnv), with the verdict, canonical path and Refuse of the row they fill.
func TestRenameTable_FlatViperKeysAreRetired(t *testing.T) {
	type entry struct {
		name      string
		file      bool
		canonical config.Path
		verdict   Verdict
	}
	const (
		write = config.Path("modules.books.itunes.library_write_path")
		read  = config.Path("modules.books.itunes.library_read_path")
	)
	want := []entry{
		{"audible_base_url", true, "", VerdictWarn},
		{"audnexus_base_url", true, "", VerdictWarn},
		{"google_books_base_url", true, "", VerdictWarn},
		{"openlibrary_base_url", true, "", VerdictWarn},
		{"AUDIBLE_BASE_URL", false, "", VerdictWarn},
		{"AUDNEXUS_BASE_URL", false, "", VerdictWarn},
		{"GOOGLE_BOOKS_BASE_URL", false, "", VerdictWarn},
		{"OPENLIBRARY_BASE_URL", false, "", VerdictWarn},
		{"itunes_library_itl_path", true, write, VerdictRefuse},
		{"itunes_library_write_path", true, write, VerdictRefuse},
		{"itunes_library_read_path", true, read, VerdictRefuse},
		{"itunes_library_xml_path", true, read, VerdictRefuse},
		{"ITUNES_LIBRARY_ITL_PATH", false, write, VerdictRefuse},
		{"ITUNES_LIBRARY_WRITE_PATH", false, write, VerdictRefuse},
		{"ITUNES_LIBRARY_READ_PATH", false, read, VerdictRefuse},
		{"ITUNES_LIBRARY_XML_PATH", false, read, VerdictRefuse},
	}
	got := map[string]config.RetiredName{}
	for _, r := range RetiredConfigNames() {
		if _, dup := got[strings.ToLower(r.Name)+boolKey(r.File)]; dup {
			t.Errorf("retired name %s (file=%v) is listed twice", r.Name, r.File)
		}
		got[strings.ToLower(r.Name)+boolKey(r.File)] = r
	}
	verdicts := map[string]Verdict{}
	for _, row := range Table().Rows {
		for _, e := range row.Retired {
			verdicts[strings.ToLower(e.Name)+boolKey(e.File)] = e.Verdict
		}
	}
	for _, w := range want {
		k := strings.ToLower(w.name) + boolKey(w.file)
		r, ok := got[k]
		if !ok {
			t.Errorf("%s (file=%v) is not a retired name", w.name, w.file)
			continue
		}
		if r.Name != w.name || r.Canonical != w.canonical || verdicts[k] != w.verdict {
			t.Errorf("%s (file=%v) = {name %s canonical %q verdict %s}, want {canonical %q verdict %s}", w.name, w.file, r.Name, r.Canonical, verdicts[k], w.canonical, w.verdict)
		}
	}

	// Every key of both alias maps is covered, so a key added to one is retired
	// without another list to remember; one that fills no row fails the generator.
	for key := range bindAlias {
		if _, ok := got[key+boolKey(true)]; !ok {
			t.Errorf("bindAlias key %s has no retired file entry", key)
		}
	}
	for key := range flatFileAlias {
		if _, ok := got[key+boolKey(true)]; !ok {
			t.Errorf("flatFileAlias key %s has no retired file entry", key)
		}
	}
	flatFileAlias["itunes_library_bogus_path"] = "itunes.no_such_leaf"
	t.Cleanup(func() { delete(flatFileAlias, "itunes_library_bogus_path") })
	if _, err := generateTable(); err == nil || !strings.Contains(err.Error(), "itunes_library_bogus_path") {
		t.Errorf("generateTable with an alias to no row = %v, want an error naming the key", err)
	}
}

func boolKey(file bool) string {
	if file {
		return "|file"
	}
	return "|env"
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
