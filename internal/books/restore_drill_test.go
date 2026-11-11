// file: internal/books/restore_drill_test.go
// version: 1.0.0
// guid: 2c7e9a41-5f08-4d3b-b916-8e4a0d6c1f75
// last-edited: 2026-11-10

package books

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/telemetry"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
)

// The restore drill (F2-15; 05-storage §5S.10 TestRollbackRestoreBootsPreviousBuild,
// §5S.3.12; 09 §9.3 "restore drill"), the TDD selftest of the cutover suite's
// checks, and the pre-freeze tag reader. See cutover_suite_test.go for the
// corpora and the checks.

// drillMarker is the playlist the drill writes after the upgrade: every
// restore must remove it.
const drillMarker = "Arkham Asylum drill marker"

// suiteStart is when this test process started: an answer's task last_run at
// or after it was written by a boot the suite drove, not by the corpus.
var suiteStart = time.Now().UTC()

// ---------------------------------------------------------------- the drill

// TestRollbackRestoreBootsPreviousBuild: after a boot of the build under test
// (which takes the pre-migration checkpoint when any step runs) and a marker
// row written after it, the checkpoint is restored offline with the build's
// storage restore-offline --checkpoint, and the pre-freeze release's binary
// boots the result: the marker is gone, every pre-upgrade row is intact, and
// it gives the corpus' pre-migration answers. The same through
// ROLLBACK_ARCHIVE, from a legacy-abo/0 archive (HEAD's backup format) made
// from the corpus. A boot that takes no checkpoint (no step and nothing else
// to run) logs it and the archive half still runs.
func TestRollbackRestoreBootsPreviousBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("boots the composition and the pre-freeze binary (extraction-gates r2-cutover-suite)")
	}
	forEachCorpus(t, func(t *testing.T, c corpus) {
		if c.name == "shaped" {
			t.Skip("the drill runs on r0 and prefreeze-written; the shaped store carries no answers")
		}
		if os.Getenv(r2PrefreezeBinEnv) == "" {
			if inCI() {
				t.Fatalf("%s is not set: the drill boots the pre-freeze binary", r2PrefreezeBinEnv)
			}
			t.Skipf("%s is not set: no pre-freeze binary to boot after the restore", r2PrefreezeBinEnv)
		}
		steps := registeredCutoverSteps(t)
		if len(steps) == 0 {
			t.Log("no cutover step registered: the drill restores R1's own boot")
		}
		want := c.answers
		if c.name == "prefreeze-written" {
			want = prefreezeAnswers(t)
		}
		pristine := copyCorpus(t, c)
		pre := scanStore(t, storeDB(pristine), wholeStore())

		t.Run("checkpoint", func(t *testing.T) {
			work := copyCorpus(t, c)
			// Only a checkpoint the upgrade boot took: a store the pre-freeze
			// release wrote already holds that release's own checkpoints,
			// taken before it wrote rule 10's rows, and restoring one of
			// those would lose them.
			had := map[string]bool{}
			for _, cp := range checkpoints(t, work) {
				had[cp] = true
			}
			upgradeAndMark(t, work)
			var taken []string
			for _, cp := range checkpoints(t, work) {
				if !had[cp] {
					taken = append(taken, cp)
				}
			}
			if len(taken) == 0 {
				require.Empty(t, steps, "a boot that runs cutover steps must take the pre-migration checkpoint")
				t.Skip("the upgrade boot took no checkpoint (no step or legacy migration to run on this corpus): the archive half restores it")
			}
			restoreOffline(t, work, "--checkpoint", taken[len(taken)-1], "--accept-loss")
			drillAfterRestore(t, c, pre, work, want)
		})
		t.Run("archive", func(t *testing.T) {
			archive := legacyArchive(t, pristine)
			work := copyCorpus(t, c)
			upgradeAndMark(t, work)
			restoreOffline(t, work, "--archive", archive, "--accept-loss")
			drillAfterRestore(t, c, pre, work, want)
		})
	})
}

// wholeStore scans every key, core ones included, with rows kept.
func wholeStore() scanSpec { return scanSpec{keepRows: true, hashCore: true} }

// upgradeAndMark runs the build under test on the copy at work to readiness
// (the upgrade), writes the drill's marker playlist through books' store
// while it serves, and stops it.
func upgradeAndMark(t *testing.T, work string) {
	t.Helper()
	edit, opt := cutoverBootConfig(t, work)
	r := runComposedIn(t, filepath.Join(work, "state"), edit, nil, opt, nil)
	_, err := r.composed.store.CreatePlaylist(drillMarker, nil, "")
	require.NoError(t, err, "the marker row")
	require.NoError(t, r.stop())
}

// restoreOffline runs the build's own `storage restore-offline` (the command
// make rollback-restore runs through its helper) in this process, with the
// options serve's offline commands use (books.OfflineOptions) for the copy.
func restoreOffline(t *testing.T, work string, args ...string) {
	t.Helper()
	stateDir := filepath.Join(work, "state")
	opts := app.Options{
		AppID:             AppID,
		Version:           telemetry.Version(),
		StateDir:          stateDir,
		R1StoragePath:     storeDB(work),
		StorageStateFiles: StateFiles(stateDir, storeDB(work)),
		R1BeforeSwap:      database.DiscardHNSWCleanStop,
		R1ArchiveExtract:  backup.ExtractStore,
		StorageFS:         storage.FS{},
	}
	for _, cmd := range app.OfflineCommands(opts, Module()) {
		if cmd.Name != "storage restore-offline" {
			continue
		}
		var out bytes.Buffer
		require.NoError(t, cmd.Run(t.Context(), args, &out), "storage restore-offline %v: %s", args, out.String())
		t.Logf("storage restore-offline %s: %s", strings.Join(args, " "), strings.TrimSpace(out.String()))
		return
	}
	t.Fatal("app.OfflineCommands has no storage restore-offline")
}

// legacyArchive makes a legacy-abo/0 archive (HEAD's backup format, the one
// every archive before R2's backup flip has) of the corpus copy's store.
func legacyArchive(t *testing.T, pristine string) string {
	t.Helper()
	src := copyCorpus(t, corpus{dir: pristine})
	info, err := backup.CreateBackup(storeDB(src), "pebble", backup.BackupConfig{BackupDir: t.TempDir(), MaxBackups: 1})
	require.NoError(t, err)
	return info.Path
}

// drillAfterRestore checks the restored copy against the pre-upgrade scan,
// then boots the pre-freeze binary on it through F2-12's recorder and
// compares its answers with want.
func drillAfterRestore(t *testing.T, c corpus, pre *storeScan, work, want string) {
	t.Helper()
	restored := scanStore(t, storeDB(work), wholeStore())
	requireNoProblems(t, "the restored store", drillProblems(pre, restored, c.wrote))
	got := filepath.Join(t.TempDir(), "answers")
	recordAnswers(t, work, got)
	requireNoProblems(t, "the pre-freeze binary's answers after the restore", answersProblems(t, want, got))
}

// drillProblems: the restored store holds no row of the marker, and every
// pre-upgrade row (the pre-freeze binary's included) is there byte for byte:
// the restore returns the store as it was before the upgrade boot.
func drillProblems(pre, restored *storeScan, wrote *prefreezeRows) []string {
	var out []string
	for _, k := range sortedKeys(restored.rows) {
		if bytes.Contains(restored.rows[k], []byte(drillMarker)) {
			out = append(out, fmt.Sprintf("the marker written after the upgrade survived the restore (%s)", k))
		}
	}
	if wrote != nil {
		for _, k := range wrote.keys() {
			if _, ok := restored.rows[k]; !ok {
				out = append(out, fmt.Sprintf("the pre-freeze binary's row %s is gone", k))
			}
		}
	}
	return append(out, byteProblems(pre, restored)...)
}

// ---------------------------------------------------------------- answers

// answersProblems compares every answer file of want with got after
// normalizing what a replay at another time cannot reproduce beyond the
// recorder's own volatile fields: a task's last_run written by a boot the
// suite drove (at or after suiteStart; a daily task's boot catch-up). The
// maintenance window needs no normalization: the replays move it with the
// boot's time zone (record_r0_answers.py --shift-window), not its settings.
func answersProblems(t *testing.T, want, got string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(want, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, files, "no answer in %s", want)
	var out []string
	for _, f := range files {
		name := filepath.Base(f)
		w, g := readAnswer(t, f), readAnswer(t, filepath.Join(got, name))
		normalizeAnswerPair(w, g)
		out = append(out, jsonDiff(name, w, g)...)
	}
	return out
}

func readAnswer(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var v map[string]any
	require.NoError(t, json.Unmarshal(raw, &v), path)
	delete(v, "r0_tag") // the recording binary's tag
	return v
}

func normalizeAnswerPair(want, got map[string]any) {
	wx, _ := want["exchanges"].([]any)
	gx, _ := got["exchanges"].([]any)
	for i := range min(len(wx), len(gx)) {
		we, _ := wx[i].(map[string]any)
		ge, _ := gx[i].(map[string]any)
		if requestPath(ge) == "/api/v1/tasks" {
			maskBootRuns(responseBody(we), responseBody(ge))
		}
	}
}

func requestPath(e map[string]any) string {
	r, _ := e["request"].(map[string]any)
	p, _ := r["path"].(string)
	return p
}

func responseBody(e map[string]any) any {
	r, _ := e["response"].(map[string]any)
	return r["body"]
}

func dig(v any, key string) any {
	m, _ := v.(map[string]any)
	return m[key]
}

// maskBootRuns drops a task's last_run from both answers when the replay's
// value is at or after suiteStart: a run of that boot (a catch-up of a daily
// task), not a row of the corpus.
func maskBootRuns(want, got any) {
	wl, _ := dig(want, "data").([]any)
	gl, _ := dig(got, "data").([]any)
	byName := map[string]map[string]any{}
	for _, x := range wl {
		if m, ok := x.(map[string]any); ok {
			n, _ := m["name"].(string)
			byName[n] = m
		}
	}
	for _, x := range gl {
		m, ok := x.(map[string]any)
		if !ok {
			continue
		}
		s, _ := m["last_run"].(string)
		at, err := time.Parse(time.RFC3339, s)
		if err != nil || at.Before(suiteStart.Truncate(time.Second)) {
			continue
		}
		delete(m, "last_run")
		n, _ := m["name"].(string)
		if w := byName[n]; w != nil {
			delete(w, "last_run")
		}
	}
}

// jsonDiff lists up to twenty paths where want and got differ.
func jsonDiff(name string, want, got any) []string {
	var out []string
	var walk func(p string, w, g any)
	walk = func(p string, w, g any) {
		if len(out) >= 20 {
			return
		}
		switch wv := w.(type) {
		case map[string]any:
			gv, ok := g.(map[string]any)
			if !ok {
				out = append(out, fmt.Sprintf("%s%s: an object became %T", name, p, g))
				return
			}
			keys := union(wv, gv)
			for _, k := range sortedKeys(keys) {
				walk(p+"."+k, wv[k], gv[k])
			}
		case []any:
			gv, ok := g.([]any)
			if !ok || len(gv) != len(wv) {
				out = append(out, fmt.Sprintf("%s%s: a list of %d became %v", name, p, len(wv), describe(g)))
				return
			}
			for i := range wv {
				walk(fmt.Sprintf("%s[%d]", p, i), wv[i], gv[i])
			}
		default:
			if !reflect.DeepEqual(w, g) {
				out = append(out, fmt.Sprintf("%s%s: %v != %v", name, p, w, g))
			}
		}
	}
	walk("", want, got)
	return out
}

func describe(v any) string {
	if l, ok := v.([]any); ok {
		return fmt.Sprintf("a list of %d", len(l))
	}
	return fmt.Sprintf("%T", v)
}

// ---------------------------------------------------------------- the pre-freeze tag

// TestPrefreezeTagResolver: fetch_prefreeze_binary.sh --resolve reads
// ci/prefreeze_release.txt by the one reader rule shared with F3-07 and F3-08
// (the single line that is neither blank nor starts with '#'): F3-08's exact
// shape resolves to its tag, and a file with no tag line or two fails.
func TestPrefreezeTagResolver(t *testing.T) {
	script := filepath.Join(r2RepoRoot(t), "scripts", "ci", "fetch_prefreeze_binary.sh")
	header := "# file: ci/prefreeze_release.txt\n# version: 1.0.0\n# guid: 00000000-0000-4000-8000-000000000000\n# last-edited: 2026-11-10\n"
	for name, tc := range map[string]struct {
		body string
		tag  string // "" = must fail
	}{
		"f3-08-shape":  {body: header + "# The pre-freeze release (09 §9.2.1); one tag line.\n\nv0.1.0\n", tag: "v0.1.0"},
		"no-tag-line":  {body: header + "# The pre-freeze release (09 §9.2.1); one tag line.\n\n"},
		"two-tags":     {body: header + "v0.1.0\nv0.2.0\n"},
		"blank-spaces": {body: header + "   \n\t\nv0.3.0\n", tag: "v0.3.0"},
	} {
		t.Run(name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "prefreeze_release.txt")
			require.NoError(t, os.WriteFile(f, []byte(tc.body), 0o600))
			out, err := exec.CommandContext(t.Context(), "bash", script, "--resolve", f).Output()
			if tc.tag == "" {
				require.Error(t, err, "resolved %q", out)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.tag+"\n", string(out))
		})
	}
}

// ---------------------------------------------------------------- the selftest

// selftestFamily is the family the fake cutover step moves: users, whose IDs
// books' other rows reference, so the ID and the referential checks have
// something to bite on.
const selftestFamily = "u:"

// selftestStepID is the fake step's data migration ID.
const selftestStepID = "books.selftest-move-users"

// fakeCase is one way the fake step runs, and the checks that must report it.
type fakeCase struct {
	name  string
	mut   string
	bites []string // checks that must report problems; every other must pass
}

// fakeCases are the negative cases (and the clean run, which every check
// passes). A step that deletes or silently loses a row passes the count rule
// (read = imported + quarantined + deleted adds up): the ID and referential
// passes are what catch it, which is why the suite has both.
// drop-prefreeze runs on the prefreeze-written corpus only.
var fakeCases = []fakeCase{
	{name: "clean"},
	{name: "reports-fewer-rows-than-its-source", mut: "undercount", bites: []string{"counts"}},
	{name: "leaves-a-row-in-its-source", mut: "leave", bites: []string{"counts"}},
	{name: "quarantines-without-counting-it", mut: "uncounted-quarantine", bites: []string{"counts"}},
	{name: "deletes-a-row", mut: "delete", bites: []string{"ids", "refs"}},
	{name: "drops-a-row-silently", mut: "drop", bites: []string{"ids", "refs"}},
	{name: "changes-an-id", mut: "change-id", bites: []string{"ids", "refs"}},
	{name: "counts-a-row-twice", mut: "double-count", bites: []string{"counts"}},
	{name: "touches-a-byte-outside-its-source", mut: "touch-outside", bites: []string{"bytes"}},
	{name: "deletes-a-family-outside-its-source", mut: "delete-family", bites: []string{"bytes"}},
	{name: "writes-a-family-the-corpus-lacks", mut: "new-family", bites: []string{"bytes"}},
	{name: "quarantines-a-row-no-case-names", mut: "quarantine", bites: []string{"quarantine"}},
	{name: "quarantines-without-reporting-orphans", mut: "quarantine-unreported", bites: []string{"quarantine", "refs"}},
	{name: "imports-a-quarantine-case", mut: "import-case", bites: []string{"quarantine"}},
	{name: "drops-a-row-the-prefreeze-binary-wrote", mut: "drop-prefreeze", bites: []string{"ids"}},
}

// testCutoverSelftest proves every check of the suite fails when a cutover
// step misbehaves, on both corpora: a fake step (moveUsers) is applied
// straight to a copy's store, and each check runs over the before and after
// scans exactly as the real tests run it. The drill's checks and the answer
// comparison are proved the same way (drill/, answers/).
func testCutoverSelftest(t *testing.T) {
	cs := []corpus{r0Corpus(t)}
	switch {
	case os.Getenv(r2PrefreezeBinEnv) != "":
		cs = append(cs, prefreezeWrittenCorpus(t))
	case inCI():
		t.Fatalf("%s is not set: the selftest runs on prefreeze-written too", r2PrefreezeBinEnv)
	}
	shared := loadSharedFamilies(t)
	for _, c := range cs {
		t.Run(c.name, func(t *testing.T) {
			pristine := copyCorpus(t, c)
			for _, fc := range fakeCases {
				if fc.mut == "drop-prefreeze" && c.wrote == nil {
					continue
				}
				t.Run(fc.name, func(t *testing.T) {
					work := copyCorpus(t, corpus{dir: pristine})
					step, referenced := moveUsers(t, storeDB(work), fc.mut, c)
					spec := scanSpec{steps: []cutoverStep{step}, shared: shared, keepRows: true}
					before, after := scanStore(t, storeDB(pristine), spec), scanStore(t, storeDB(work), spec)
					steps := []cutoverStep{step}
					got := map[string][]string{
						"counts":     countProblems(steps, before, after),
						"quarantine": quarantineProblems(steps, c.quarantineCases, after.Quarantine),
						"ids":        idProblems(before, after),
						"refs":       refProblems(steps, before, after),
						"bytes":      byteProblems(before, after),
					}
					bites := fc.bites
					if fc.mut == "drop-prefreeze" && referenced {
						bites = append(slices.Clone(bites), "refs")
					}
					requireBites(t, got, bites)
				})
			}
			t.Run("drill", func(t *testing.T) { testDrillSelftest(t, c, pristine) })
		})
	}
	t.Run("answers", testAnswersSelftest)
}

// requireBites asserts the named checks report a problem and the others none.
func requireBites(t *testing.T, got map[string][]string, bites []string) {
	t.Helper()
	for _, check := range sortedKeys(got) {
		if slices.Contains(bites, check) {
			require.NotEmptyf(t, got[check], "the %s check did not catch the fake step", check)
		} else {
			require.Emptyf(t, got[check], "the %s check reported a correct fake step", check)
		}
	}
}

// moveUsers is the fake cutover step: a fake importer moves every u: row to
// c/selftest/user/<id> (IDs kept) and deletes the source, quarantines the
// users the overlay's quarantine cases name (a record each, orphaned
// references counted per referencing family), and reports its counts, all in
// one batch. mut makes it misbehave on one victim row: a user other rows
// reference, or the pre-freeze binary's user for drop-prefreeze. It returns
// the step and whether the victim is referenced.
func moveUsers(t *testing.T, dir, mut string, c corpus) (cutoverStep, bool) {
	t.Helper()
	db, err := pebble.Open(dir, &pebble.Options{})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	step := cutoverStep{ID: selftestStepID, Source: []keyRange{prefixRange(selftestFamily)}}
	rows := map[string][]byte{}
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: step.Source[0].Lo, UpperBound: step.Source[0].Hi})
	require.NoError(t, err)
	for it.First(); it.Valid(); it.Next() {
		rows[string(it.Key())] = bytes.Clone(it.Value())
	}
	require.NoError(t, it.Close())
	keys := sortedKeys(rows)
	require.NotEmpty(t, keys, "the corpus holds no %s row", selftestFamily)

	caseRows := map[string]bool{}
	for _, qc := range c.quarantineCases {
		for _, k := range qc.Keys {
			if row, _, _ := strings.Cut(k, "#"); step.inSource([]byte(row)) {
				caseRows[row] = true
			}
		}
	}
	refs := map[string]map[string]int64{}
	for _, k := range keys {
		refs[k] = referencingFamilies(t, db, strings.TrimPrefix(k, selftestFamily))
	}
	victim := ""
	switch mut {
	case "drop-prefreeze":
		victim = selftestFamily + c.wrote.User.ID
		require.Contains(t, rows, victim, "the pre-freeze binary's user is not in the corpus")
	case "import-case", "uncounted-quarantine":
		require.NotEmpty(t, caseRows, "no overlay quarantine case names a %s row", selftestFamily)
		victim = sortedKeys(caseRows)[0]
	case "":
	default:
		for _, k := range keys {
			if !caseRows[k] && len(refs[k]) > 0 {
				victim = k
				break
			}
		}
		require.NotEmpty(t, victim, "no %s row other rows reference: the ID and referential checks would prove nothing", selftestFamily)
	}

	b := db.NewBatch()
	r := &step.Report
	r.OrphanedRefs = map[string]int64{}
	n := 0
	quarantine := func(k string, reportOrphans bool) {
		n++
		rec, err := json.Marshal(quarantineRecord{Key: []byte(k), Step: step.ID, Reason: "selftest: the fake importer refused it"})
		require.NoError(t, err)
		require.NoError(t, b.Set(fmt.Appendf(nil, "c/storage/quarantine/%s/%06d", step.ID, n), rec, nil))
		if reportOrphans {
			for f, m := range refs[k] {
				r.OrphanedRefs[f] += m
			}
		}
	}
	for _, k := range keys {
		id := strings.TrimPrefix(k, selftestFamily)
		r.Read++
		isVictim := k == victim
		switch {
		case isVictim && mut == "leave":
			r.Imported++ // claimed, never drained
			continue
		case isVictim && mut == "uncounted-quarantine":
			quarantine(k, true)
			r.Imported++ // a record written, the report says imported
		case caseRows[k] && !(isVictim && mut == "import-case"):
			quarantine(k, true)
			r.Quarantined++
		case isVictim && mut == "delete":
			r.Deleted++ // reported, but users are no deleted-by-rule family
		case isVictim && (mut == "drop" || mut == "drop-prefreeze"):
			r.Imported++ // reported imported, never written
		case isVictim && (mut == "quarantine" || mut == "quarantine-unreported"):
			quarantine(k, mut == "quarantine")
			r.Quarantined++
		case isVictim && mut == "change-id":
			r.Imported++
			require.NoError(t, b.Set([]byte("c/selftest/user/"+id[:len(id)-1]+flip(id[len(id)-1])), rows[k], nil))
		default:
			r.Imported++
			require.NoError(t, b.Set([]byte("c/selftest/user/"+id), rows[k], nil))
		}
		require.NoError(t, b.Delete([]byte(k), nil))
	}
	switch mut {
	case "undercount":
		r.Read--
		r.Imported--
	case "double-count":
		r.Imported++
	case "touch-outside":
		k, v := firstKey(t, db, "author:")
		require.NoError(t, b.Set(k, append(v, ' '), nil))
	case "delete-family":
		k, _ := firstKey(t, db, "bookmark:")
		rng := prefixRange("bookmark:")
		require.NoError(t, b.DeleteRange(rng.Lo, rng.Hi, nil), "the family of %s", k)
	case "new-family":
		rng := prefixRange("playlist:")
		it, err := db.NewIter(&pebble.IterOptions{LowerBound: rng.Lo, UpperBound: rng.Hi})
		require.NoError(t, err)
		require.False(t, it.First(), "the corpus holds a playlist: row: pick another absent family")
		require.NoError(t, it.Close())
		require.NoError(t, b.Set([]byte("playlist:01ARKHAM0000000000000000"), []byte(`{"name":"Arkham Knight"}`), nil))
	}
	require.NoError(t, b.Commit(pebble.Sync))
	return step, victim != "" && len(refs[victim]) > 0
}

// flip returns another ULID digit than c.
func flip(c byte) string {
	if c == 'Z' {
		return "Y"
	}
	return "Z"
}

// referencingFamilies counts, per books family, the rows whose value holds id
// as a quoted string (the referential pass' rule), u: excluded.
func referencingFamilies(t *testing.T, db *pebble.DB, id string) map[string]int64 {
	t.Helper()
	it, err := db.NewIter(nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, it.Close()) }()
	fi := newFamilyIndex()
	out := map[string]int64{}
	needle := []byte(`"` + id + `"`)
	for it.First(); it.Valid(); it.Next() {
		f := fi.family(it.Key())
		if f == selftestFamily || coreFamily(f) {
			continue
		}
		if bytes.Contains(it.Value(), needle) {
			out[f]++
		}
	}
	return out
}

func firstKey(t *testing.T, db *pebble.DB, prefix string) ([]byte, []byte) {
	t.Helper()
	r := prefixRange(prefix)
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: r.Lo, UpperBound: r.Hi})
	require.NoError(t, err)
	defer func() { require.NoError(t, it.Close()) }()
	require.True(t, it.First(), "no %s row", prefix)
	return bytes.Clone(it.Key()), bytes.Clone(it.Value())
}

// testDrillSelftest proves drillProblems bites: a "restored" store that is
// the pre-upgrade copy passes; one that still holds the marker, one whose
// checkpoint was taken after the fake step drained users, and (on
// prefreeze-written) one missing the pre-freeze binary's user each fail.
func testDrillSelftest(t *testing.T, c corpus, pristine string) {
	pre := scanStore(t, storeDB(pristine), wholeStore())
	for name, tc := range map[string]struct {
		mutate func(t *testing.T, db *pebble.DB)
		want   string // a problem naming this; "" = none
	}{
		"restore-is-the-pre-upgrade-store": {mutate: func(*testing.T, *pebble.DB) {}},
		"marker-survives": {want: "the marker written after the upgrade survived", mutate: func(t *testing.T, db *pebble.DB) {
			require.NoError(t, db.Set([]byte("playlist:999999"), []byte(`{"name":"`+drillMarker+`"}`), pebble.Sync))
		}},
		"checkpoint-after-the-step": {want: "family u:: all", mutate: nil},
		"prefreeze-row-lost": {want: "the pre-freeze binary's row", mutate: func(t *testing.T, db *pebble.DB) {
			if c.wrote == nil {
				t.Skip("prefreeze-written only")
			}
			require.NoError(t, db.Delete([]byte("u:"+c.wrote.User.ID), pebble.Sync))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			work := copyCorpus(t, corpus{dir: pristine})
			if tc.mutate == nil {
				moveUsers(t, storeDB(work), "", c)
			} else {
				db, err := pebble.Open(storeDB(work), &pebble.Options{})
				require.NoError(t, err)
				tc.mutate(t, db)
				require.NoError(t, db.Close())
			}
			got := drillProblems(pre, scanStore(t, storeDB(work), wholeStore()), c.wrote)
			if tc.want == "" {
				require.Empty(t, got)
				return
			}
			require.Truef(t, slices.ContainsFunc(got, func(p string) bool { return strings.Contains(p, tc.want) }),
				"drillProblems did not report %q: %v", tc.want, got)
		})
	}
}

// testAnswersSelftest proves answersProblems bites on a changed answer and
// lets through only what it normalizes (a boot's last_run).
func testAnswersSelftest(t *testing.T) {
	want := r0Corpus(t).answers
	for name, tc := range map[string]struct {
		edit  func(name string, v map[string]any)
		bites bool
	}{
		"identical": {edit: func(string, map[string]any) {}},
		"the-window-moved": {bites: true, edit: func(name string, v map[string]any) {
			if name == "schedules.json" {
				for _, e := range v["exchanges"].([]any) {
					if requestPath(e.(map[string]any)) == "/api/v1/maintenance-window/status" {
						dig(responseBody(e.(map[string]any)), "data").(map[string]any)["window_start"] = 14.0
					}
				}
			}
		}},
		"a-boot-ran-a-task": {edit: func(name string, v map[string]any) {
			if name == "schedules.json" {
				task := dig(responseBody(v["exchanges"].([]any)[0].(map[string]any)), "data").([]any)[0].(map[string]any)
				task["last_run"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
			}
		}},
		"a-user-is-gone": {bites: true, edit: func(name string, v map[string]any) {
			if name == "identity.json" {
				body := responseBody(v["exchanges"].([]any)[0].(map[string]any))
				if l, ok := dig(body, "data").([]any); ok && len(l) > 0 {
					body.(map[string]any)["data"] = l[1:]
					return
				}
				body.(map[string]any)["data"] = "changed"
			}
		}},
		// A last_run older than the suite came from the store, not a boot of
		// the suite: the normalization must not hide it.
		"a-task-gained-a-run-before-the-suite": {bites: true, edit: func(name string, v map[string]any) {
			if name == "schedules.json" {
				task := dig(responseBody(v["exchanges"].([]any)[0].(map[string]any)), "data").([]any)[0].(map[string]any)
				task["last_run"] = suiteStart.Add(-time.Hour).Format(time.RFC3339)
			}
		}},
		"a-task-was-disabled": {bites: true, edit: func(name string, v map[string]any) {
			if name == "schedules.json" {
				task := dig(responseBody(v["exchanges"].([]any)[0].(map[string]any)), "data").([]any)[1].(map[string]any)
				task["enabled"] = !task["enabled"].(bool)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			got := t.TempDir()
			files, err := filepath.Glob(filepath.Join(want, "*.json"))
			require.NoError(t, err)
			sort.Strings(files)
			for _, f := range files {
				v := readAnswer(t, f)
				tc.edit(filepath.Base(f), v)
				raw, err := json.Marshal(v)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(got, filepath.Base(f)), raw, 0o600))
			}
			problems := answersProblems(t, want, got)
			if tc.bites {
				require.NotEmpty(t, problems)
			} else {
				require.Empty(t, problems)
			}
		})
	}
}
