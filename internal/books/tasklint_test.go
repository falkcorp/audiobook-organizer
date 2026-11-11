// file: internal/books/tasklint_test.go
// version: 1.0.1
// guid: 710e9000-635d-4826-8047-68b02275e9ca
// last-edited: 2026-11-10

package books

import (
	"testing"

	"github.com/falkcorp/mediacore/archtest/tasklint"
)

const (
	aboModule = "github.com/falkcorp/audiobook-organizer"
	// tasklintExempt is the dark-conversion ratchet (05-tasks §5T.8.4): one
	// entry file per conversion row plus F5-18.txt, read as one union.
	tasklintExempt = "testdata/tasklint_exempt.d"
)

// tasklintPackages are books' task packages: every directory that holds an
// entry-file line, plus internal/books/... F5-18 replaces this list with the
// set its Registrar.Tasks call derives.
func tasklintPackages() []string {
	return []string{
		aboModule + "/internal/books/...",
		aboModule + "/internal/maintenance/jobs",
		aboModule + "/internal/plugins/acoustid",
		aboModule + "/internal/plugins/dedup",
		aboModule + "/internal/plugins/deluge",
		aboModule + "/internal/plugins/itunes",
		aboModule + "/internal/plugins/maintenance",
		aboModule + "/internal/plugins/metafetch",
		aboModule + "/internal/repairs",
		aboModule + "/internal/scheduler",
		aboModule + "/internal/server",
	}
}

// tasklintOptions is ABO's run. LegacyDefinitions makes HEAD's definition
// forms (v2 OperationDef, the pkg/ops specs) definitions for writer-bypass and
// store-capture, as oplint's probe-v2 did; F5-18 deletes it with HEAD's engine.
// ListFuncs counts books' list functions as registration roots until the flip
// makes ABO's first Registrar.Tasks call (05-tasks §5T.8.2 rule 4).
func tasklintOptions() tasklint.Options {
	return tasklint.Options{
		Packages:      tasklintPackages(),
		ModulePath:    aboModule,
		StorePackages: []string{aboModule + "/internal/books/store"},
		ExemptDir:     tasklintExempt,
		ListFuncs:     true,
		LegacyDefinitions: []string{
			aboModule + "/internal/operations/registry.OperationDef",
			aboModule + "/pkg/ops.TaskSpec",
			aboModule + "/pkg/ops.BatchSpec",
			aboModule + "/pkg/ops.FixerSpec",
		},
	}
}

// TestTasklint_Books: tasklint's six rules over books' task packages pass
// only through the exemption union, and every union line names an existing
// file (05-tasks §5T.8.4).
func TestTasklint_Books(t *testing.T) {
	tasklint.Run(t, tasklintOptions())
}

// TestTasklint_F518ListsOnlyFlaggedFiles: F5-18.txt holds exactly the files
// that have a writer-bypass or store-capture finding and that no conversion
// row lists; a line without a finding there is an entry nothing needs.
func TestTasklint_F518ListsOnlyFlaggedFiles(t *testing.T) {
	o := tasklintOptions()
	cfg := tasklint.DefaultConfig()
	cfg.ModulePath, cfg.StorePackages, cfg.ListFuncs, cfg.LegacyDefinitions = o.ModulePath, o.StorePackages, o.ListFuncs, o.LegacyDefinitions
	findings, _, err := tasklint.Analyze(cfg, "../..", o.Packages)
	if err != nil {
		t.Fatal(err)
	}
	flagged := map[string]bool{}
	for _, f := range findings {
		if f.Category == tasklint.CatWriterBypass || f.Category == tasklint.CatStoreCapture {
			flagged[f.File] = true
		}
	}
	union, problems, err := tasklint.ReadUnion(tasklintExempt)
	if err != nil || len(problems) > 0 {
		t.Fatalf("read the union: %v %v", err, problems)
	}
	if len(flagged) == 0 {
		t.Fatal("no writer-bypass or store-capture finding at all: LegacyDefinitions or the package list is broken, not the code")
	}
	for file, owner := range union {
		if owner == "F5-18.txt" && !flagged[file] {
			t.Errorf("F5-18.txt lists %s, which has no writer-bypass or store-capture finding; delete the line", file)
		}
	}
}

// TestTasklintExemptions_ShrinkOnly: the entry files may only shrink against
// the merge base with origin/main (origin/main itself in CI's shallow
// checkout, whose workflow fetches it at depth 1); an added file or line fails
// (05-tasks §5T.8.4). Without git or origin/main it skips locally and fails
// under CI=true. At F2-17's own PR the base has no directory, so it passes.
func TestTasklintExemptions_ShrinkOnly(t *testing.T) {
	tasklint.CheckShrinkOnly(t, tasklintExempt)
}
