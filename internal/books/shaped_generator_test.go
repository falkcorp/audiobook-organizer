// file: internal/books/shaped_generator_test.go
// version: 0.1.0
// guid: 3c9e7b52-8a14-4d06-b2f1-5e6d0a9c4b17
// last-edited: 2026-11-28

package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
)

// shapedGeneratorScale is the cell's fraction of the P1 counts: enough rows of
// every grown family that a clone rule which breaks M3 shows (the API keys
// alone are a few dozen), small enough to boot and scan in seconds, and large
// enough that every grown family's clones carry their templates' values. At
// 0.03, stage 1's idx: values already exceeded the family's share of the P1
// value bytes, so its clones were written empty and the referential pass saw
// none of what they referenced: the idx:user: clones that failed the scale-1
// job passed here.
const shapedGeneratorScale = "0.1"

// shapedGeneratedCorpus names the generated store in the suite's failures.
const shapedGeneratedCorpus = "shaped-generated"

// shapedGeneratorCellEnv set to 1 runs TestShapedGenerator_StorePassesTheCutoverSuite;
// scripts/ci/run_production_shaped_store.sh sets it and requires the PASS
// line, as the /shaped subtests need R2_SHAPED_STORE.
const shapedGeneratorCellEnv = "R2_SHAPED_GENERATOR_CELL"

// TestShapedGenerator_StorePassesTheCutoverSuite: the production-shaped store
// F2-14's generator grows from stage 1 boots past M3's preflight with no
// blocker, and the cutover suite's checks of the production-shaped-store job
// hold on it: the steps complete, per-family counts and M3's stored counts
// hold, every ID is kept, every reference resolves, and the families outside
// the steps are byte-identical. Each was once broken by the generator alone
// and found only by the scale-1 job, 35 minutes in or never:
//   - an API key token hash padded until it was no SHA-256, an identity
//     link's created_at padded until it was no timestamp: blockers, so the
//     job stopped at P4a before any cutover cell ran;
//   - clone IDs led by "~", a byte keyTokens splits on: 576 IDs "gone";
//   - idx:user: rows cloned to idx:z…, outside M3's Source, still naming the
//     _system ID M3 remaps: references that resolve nowhere.
//
// The store is the generator's own output, written the way
// scripts/r2corpus/gen_production_shaped.py writes it (stage 1 copied, then
// the row writer over it with the overlay as the template source), at a small
// scale.
//
// It boots the composition twice and scans about 4.5M keys: about a minute,
// and ten under -race, so it runs only where shapedGeneratorCellEnv asks for
// it, not in the -short suite or make test-fixtures' -race shards (which run
// every short-skipped test). production-shaped-store runs it first
// (scripts/ci/run_production_shaped_store.sh), before it spends half an hour
// on the full-size store; that job's path trigger covers the generator, the
// cutover steps, the suite and the corpus.
func TestShapedGenerator_StorePassesTheCutoverSuite(t *testing.T) {
	if os.Getenv(shapedGeneratorCellEnv) != "1" {
		t.Skipf("%s is not 1: the cell grows and boots a 4.5M-key store (extraction-gates production-shaped-store sets it)", shapedGeneratorCellEnv)
	}
	root := r2RepoRoot(t)
	goBin, err := exec.LookPath("go")
	require.NoError(t, err, "the cell builds the generator: no go command on PATH")
	exe := filepath.Join(t.TempDir(), "shaped")
	build := exec.Command(goBin, "build", "-tags", "r2corpus_shaped", "-o", exe, "./scripts/r2corpus/shaped")
	build.Dir = root
	out, err := build.CombinedOutput()
	require.NoError(t, err, "build the shaped generator:\n%s", out)

	corpusDir := filepath.Join(root, "internal", "books", r2corpusDir)
	work := t.TempDir()
	require.NoError(t, copyTree(filepath.Join(corpusDir, "stage1"), work))
	gen := exec.Command(exe,
		"-mode", "generate",
		"-store", filepath.Join(work, "db", "audiobooks.pebble"),
		"-counts", filepath.Join(corpusDir, "p1_counts.json"),
		"-scale", shapedGeneratorScale,
		"-templates", filepath.Join(corpusDir, "overlay", "db", "audiobooks.pebble"),
		"-workers", "2",
		"-report", filepath.Join(t.TempDir(), "generator_report.json"),
	)
	out, err = gen.CombinedOutput()
	require.NoError(t, err, "the shaped generator failed:\n%s", out)
	requireClonesCarryValues(t, gen.Args[len(gen.Args)-1])

	// The preflight boot runs on a copy and is closed before runCutover boots
	// the store again.
	generated := corpus{name: shapedGeneratedCorpus, dir: work}
	t.Run("preflight", func(t *testing.T) {
		boot := copyCorpus(t, generated)
		edit, opt := cutoverBootConfig(t, boot)
		mod, opts, _ := composedOptions(t, false, filepath.Join(boot, "state"), edit, nil)
		opt(&opts)
		opts.Env = withLoopbackListen(opts.Env)
		// Options asks for the process defaults, which app.New installs early
		// and only InstallProcessDefaults' restore (or a failed New) puts
		// back: this boot, merely closed, would leave the process holding
		// them, and every later boot in the package (runCutover's here) would
		// answer ErrProcessDefaultsTaken. It reads the store; it needs no
		// globals (flip_older_builds_test.go's preFlip does the same).
		opts.ProcessDefaults = false
		a, err := app.New(t.Context(), opts, mod)
		defer Release(mod)
		if a != nil {
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(t, a.Close(ctx))
			}()
		}
		var cbe *storage.CutoverBlockedError
		if errors.As(err, &cbe) {
			t.Fatalf("%s: the store stops at the cutover preflight with %d blocker(s):\n%s", shapedGeneratedCorpus, len(cbe.Blockers), groupBlockers(cbe.Blockers))
		}
		require.NoError(t, err, "%s: the boot over the store failed", shapedGeneratedCorpus)
	})
	require.False(t, t.Failed(), "%s: the preflight boot failed", shapedGeneratedCorpus)

	r := runCutover(t, generated)
	must := func(what string, problems []string) {
		t.Helper()
		requireNoProblems(t, shapedGeneratedCorpus+": "+what, problems)
	}
	must("the step records", r.storedProblems)
	must("the steps completed", completionProblems(r.owner, r.steps, r.otherDMs, r.before, r.after))
	must("the level step", levelProblems(r.owner, r.steps, r.projection, r.before, r.after))
	must("per-family counts", countProblems(r.steps, r.before, r.after))
	must("M3's stored counts", identityCountProblems(r.steps, r.before, r.after))
	must("IDs", idProblems(r.before, r.after))
	must("references", refProblems(t, storeDB(r.work), r.steps, r.before, r.after))
	must("byte identity", byteProblems(r.before, r.after))
	must("the legacy bookkeeping (grow-only)", bookkeepingProblems(r.before, r.after))
	t.Logf("%s: %d steps; %d keys before, %d after", shapedGeneratedCorpus, len(r.steps), r.before.Total, r.after.Total)
}

// groupBlockers is one line per step, family and reason, with a count and one
// example key.
func groupBlockers(bs []storage.CutoverBlocker) string {
	count := map[string]int{}
	example := map[string]string{}
	for _, b := range bs {
		k := b.Step + " | " + b.Family + " | " + b.Reason
		count[k]++
		if _, ok := example[k]; !ok {
			example[k] = string(b.Key)
		}
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "  %d x %s (e.g. %q)\n", count[k], k, example[k])
	}
	return sb.String()
}

// requireClonesCarryValues fails when the generator wrote any grown family's
// clones with empty values (its "generated values are empty" note): the ID
// and referential checks would see nothing those rows reference.
func requireClonesCarryValues(t *testing.T, reportPath string) {
	t.Helper()
	raw, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	var rep struct {
		Families []struct {
			Prefix        string   `json:"prefix"`
			GeneratedKeys int64    `json:"generated_keys"`
			Notes         []string `json:"notes"`
		} `json:"families"`
	}
	require.NoError(t, json.Unmarshal(raw, &rep))
	var grown int
	var empty []string
	for _, f := range rep.Families {
		if f.GeneratedKeys == 0 {
			continue
		}
		grown++
		for _, n := range f.Notes {
			if strings.Contains(n, "generated values are empty") {
				empty = append(empty, f.Prefix)
			}
		}
	}
	require.NotZero(t, grown, "%s: the generator grew no family", shapedGeneratedCorpus)
	require.Empty(t, empty, "%s: at scale %s these families' clones are written empty, so no check sees what their values reference: raise shapedGeneratorScale", shapedGeneratedCorpus, shapedGeneratorScale)
}
