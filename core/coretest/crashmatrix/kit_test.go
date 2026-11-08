// file: core/coretest/crashmatrix/kit_test.go
// version: 1.0.5
// guid: 67d91a53-bcd6-4094-acf4-c145ba475897
// last-edited: 2026-11-08

package crashmatrix

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// cardLabel is the joker module's writer label when a test registers it.
const cardLabel = "joker.card"

// joker is a module whose Register writes one file, a card, into the state
// directory: through the boot filesystem under label (no region when label
// is empty), or past it with os.WriteFile when real is set. It declares a
// keyspace so the boot opens the store and the matrix breaks core's
// writers around the card's, as an app's boot does.
type joker struct {
	dir   string
	label string
	real  bool
}

func (j joker) Manifest() module.Manifest {
	return module.Manifest{
		ID: "joker", Version: "1.0.0", DisplayName: "Joker", MinCoreVersion: "0.1.0", DefaultEnabled: true,
		Storage: storage.Spec{Keyspace: true},
	}
}

func (j joker) Register(r *module.Registrar) error {
	path := filepath.Join(j.dir, "joker-card")
	if j.real {
		return os.WriteFile(path, []byte("ha"), 0o600)
	}
	ctx := r.Context()
	if j.label != "" {
		defer bootseam.Region(ctx, j.label)()
	}
	f, err := bootseam.FS(ctx).Create(path, vfs.WriteCategoryUnspecified)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte("ha")); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// reports is a reporter that keeps the kit's failures for the test to read.
type reports struct {
	mu   sync.Mutex
	errs []string
}

func (r *reports) Helper() {}

func (r *reports) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *reports) Logf(string, ...any) {}

// jokerScenario boots the joker module over a fresh fixture.
func jokerScenario(t *testing.T, label string, real bool, rows ...Row) Scenario {
	dir := t.TempDir()
	return Scenario{
		Build: func(_ testing.TB, inj *Injector) error {
			_, err := inj.New(context.Background(), app.Options{AppID: "gotham", Logger: slog.New(slog.DiscardHandler)},
				joker{dir: dir, label: label, real: real})
			return err
		},
		Fixtures: []Fixture{{Name: "arkham", StateDir: dir}},
		Rows:     rows,
	}
}

// TestKitFailsOnUnmatchedWriters is the kit's own self-test (R1-21 step 1):
// a boot writer added without a matrix row, a durable operation outside any
// writer's region, and a write past the boot filesystem each fail the run,
// and the same writer with its row registered passes, so the three failures
// are the writer's and not the boot's.
func TestKitFailsOnUnmatchedWriters(t *testing.T) {
	t.Parallel()
	row := Row{Phase: "P5 (test)", Writer: "joker: a card in the state directory", Labels: []string{cardLabel}}
	cases := []struct {
		name  string
		label string
		real  bool
		rows  []Row
		m     mode
		want  string // a substring of a report; "" wants none
	}{
		{name: "writer without a row", label: cardLabel, want: "label not in the writer table (Rows): " + cardLabel},
		{name: "unlabelled operation", want: "unlabelled durable operation: (unlabelled)"},
		{name: "write past the boot filesystem", real: true, want: "tree diff: the boot wrote to the real disk"},
		{name: "registered writer, census", label: cardLabel, rows: []Row{row}},
		{name: "registered writer, every kill and error", label: cardLabel, rows: []Row{row}, m: modeFull},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rep := &reports{}
			observed := run(t, rep, jokerScenario(t, tc.label, tc.real, tc.rows...), tc.m)
			got := strings.Join(rep.errs, "\n")
			switch {
			case tc.want == "" && len(rep.errs) > 0:
				t.Fatalf("the kit failed a registered writer:\n%s", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Fatalf("the kit's reports lack %q:\n%s", tc.want, got)
			}
			if tc.label != "" && !observed[tc.label] {
				t.Errorf("Observed lacks the writer's label %q", tc.label)
			}
		})
	}
}

// trickLabel is the trickster's writer label. The kit kills nested boots at
// the swap's labels only (nested), so the trickster writes under one.
const trickLabel = "swap.trickster"

// trickster is a module whose Register leaves a mark while it works and
// whose recovery from a mark it finds is not crash safe: it lays a trap
// before it clears the mark, and a boot that finds the trap fails. The
// recovery runs only in the boot after a broken one, so no census kill
// reaches the window that leaves the trap; a nested kill does.
type trickster struct{ dir string }

func (j trickster) Manifest() module.Manifest {
	return module.Manifest{
		ID: "trickster", Version: "1.0.0", DisplayName: "Trickster", MinCoreVersion: "0.1.0", DefaultEnabled: true,
		Storage: storage.Spec{Keyspace: true},
	}
}

func (j trickster) Register(r *module.Registrar) error {
	ctx := r.Context()
	defer bootseam.Region(ctx, trickLabel)()
	fs := bootseam.FS(ctx)
	mark, trap := filepath.Join(j.dir, "trick-mark"), filepath.Join(j.dir, "trick-trap")
	touch := func(p string) error {
		f, err := fs.Create(p, vfs.WriteCategoryUnspecified)
		if err != nil {
			return err
		}
		if err := errors.Join(f.Sync(), f.Close()); err != nil {
			return err
		}
		return syncDirOf(fs, p)
	}
	remove := func(p string) error {
		if err := fs.Remove(p); err != nil {
			return err
		}
		return syncDirOf(fs, p)
	}
	if _, err := fs.Stat(trap); err == nil {
		return errors.New("trickster: the trap is sprung")
	}
	if _, err := fs.Stat(mark); err == nil {
		for _, step := range []func() error{func() error { return touch(trap) }, func() error { return remove(mark) }, func() error { return remove(trap) }} {
			if err := step(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := touch(mark); err != nil {
		return err
	}
	return remove(mark)
}

func syncDirOf(fs vfs.FS, p string) error {
	d, err := fs.OpenDir(filepath.Dir(p))
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// TestKitKillsTheBootAfterABrokenOne (R1-21 review M6): the boot after a
// kill, and the rest of a boot after an injected error with its next boot,
// are killed again at each of their nested kill points, so a recovery
// writer that is not crash safe fails the run though no fault-free boot
// runs it.
func TestKitKillsTheBootAfterABrokenOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := Scenario{
		Build: func(_ testing.TB, inj *Injector) error {
			_, err := inj.New(context.Background(), app.Options{AppID: "gotham", Logger: slog.New(slog.DiscardHandler)}, trickster{dir: dir})
			return err
		},
		Fixtures: []Fixture{{Name: "arkham", StateDir: dir}},
		Rows:     []Row{{Phase: "P5 (test)", Writer: "trickster: a mark and its unsafe recovery", Labels: []string{trickLabel}}},
	}
	rep := &reports{}
	run(t, rep, s, modeFull)
	got := strings.Join(rep.errs, "\n")
	for _, want := range []string{
		"then kill before reboot 1's " + trickLabel,
		"then kill before boot 2's " + trickLabel,
		"I5: no readiness",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the kit's reports lack %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "the fault-free boot") {
		t.Fatalf("the fault-free boot failed, so the nested reports prove nothing:\n%s", got)
	}
}

// TestUnobservedNamesEveryLabel (R1-21 review M4): every label no
// fault-free boot reaches is missing, though another label of its row is
// reached, unless the allowlist gives a reason; an allowlist entry that is
// reached, or that no row lists, is stale.
func TestUnobservedNamesEveryLabel(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{Writer: "batcave", Labels: []string{"cave.mkdir", "cave.sync", "cave.rollback"}},
		{Writer: "wayne manor", Labels: []string{"manor.write"}},
	}
	missing, stale := Unobserved(rows, map[string]bool{"cave.sync": true}, nil)
	if !slices.Equal(missing, []string{"cave.mkdir", "cave.rollback", "manor.write"}) || len(stale) != 0 {
		t.Fatalf("Unobserved = %v, %v; want every unreached label", missing, stale)
	}
	allow := map[string]string{"cave.rollback": "only after a failed commit", "cave.sync": "reached", "arkham.gate": "no row"}
	missing, stale = Unobserved(rows, map[string]bool{"cave.sync": true, "cave.mkdir": true, "manor.write": true}, allow)
	if len(missing) != 0 || !slices.Equal(stale, []string{"arkham.gate", "cave.sync"}) {
		t.Fatalf("Unobserved = %v, %v; want none missing and the reached and unlisted entries stale", missing, stale)
	}
}

// TestReadSlotSkipsDirectoriesWithNoSwap (R1-21 review n3): readSlot leaves
// out a restore/<id>/ with neither a journal nor an in-flight file, which
// holds no swap, and keeps one with an in-flight file and no journal, at
// step "", so I2 reports it.
func TestReadSlotSkipsDirectoriesWithNoSwap(t *testing.T) {
	mem := vfs.NewMem()
	state := "/gotham/state"
	write := func(path, body string) {
		t.Helper()
		if err := mem.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := mem.Create(path, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.MkdirAll(restorejournal.JournalDir(state, "r-leftover"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(restorejournal.JournalDir(state, "r-done"), restorejournal.JournalName), `{"step":"applied"}`+"\n")
	journals, pending, err := readSlot(mem, state)
	if err != nil || pending {
		t.Fatalf("readSlot = %v, %v, %v", journals, pending, err)
	}
	if _, ok := journals["r-leftover"]; ok || journals["r-done"].last != "applied" || len(journals) != 1 {
		t.Fatalf("journals = %+v, want r-done only", journals)
	}
	intent := `{"id":"r-orphan","kind":"archive","db_path":"/batcave/db/main.pebble"}`
	write(filepath.Join(restorejournal.JournalDir(state, "r-orphan"), restorejournal.InflightName), intent)
	journals, _, err = readSlot(mem, state)
	if err != nil {
		t.Fatal(err)
	}
	if j, ok := journals["r-orphan"]; !ok || j.last != "" || !j.inflight {
		t.Fatalf("journals = %+v, want r-orphan in flight at step \"\"", journals)
	}
}

// TestI2TiesTheEndToTheStore (R1-21 review M5): I2 holds a run's end class
// and the store at the DB path together. A swap that ended back with the
// placed store live, or applied with the original live, is a breach; so is
// an applied end whose previous/ lost the original store, a restore that
// ended back with no staged store left, and two journals with different
// ends. The legal pairings pass.
func TestI2TiesTheEndToTheStore(t *testing.T) {
	const id = "r-archive"
	fr := &fixtureRun{initial: "original", hadStore: true,
		intent: &restorejournal.Intent{ID: id, Kind: restorejournal.KindArchive},
		legal: []outcome{
			{end: endApplied, endID: id, store: true, rows: "restored", previous: "original"},
			{end: endBack, store: true, rows: "original"},
		}}
	at := func(step string) map[string]journal { return map[string]journal{id: {last: step}} }
	applied, back := at(restoreswap.StepApplied), at(restoreswap.StepSwappedBack)
	i2 := func(o outcome) []string {
		var out []string
		for _, v := range fr.violations(o) {
			if strings.HasPrefix(v, "I2:") {
				out = append(out, v)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		o      outcome
		breach bool
	}{
		{"applied with the placed store", outcome{end: endApplied, endID: id, store: true, rows: "restored", previous: "original", journals: applied}, false},
		{"back with the original store", outcome{end: endBack, endID: id, store: true, rows: "original", stagedKept: true, journals: back}, false},
		{"back with the placed store", outcome{end: endBack, endID: id, store: true, rows: "restored", stagedKept: true, journals: back}, true},
		{"applied with the original store", outcome{end: endApplied, endID: id, store: true, rows: "original", previous: "original", journals: applied}, true},
		{"applied with previous/ lost", outcome{end: endApplied, endID: id, store: true, rows: "restored", journals: applied}, true},
		{"back with the staged store lost", outcome{end: endBack, endID: id, store: true, rows: "original", journals: back}, true},
		{"two journals, two ends", outcome{end: endMixed, store: true, rows: "original", stagedKept: true,
			journals: map[string]journal{id: {last: restoreswap.StepSwappedBack}, "r-older": {last: restoreswap.StepApplied}}}, true},
	} {
		got := i2(tc.o)
		switch {
		case tc.breach && len(got) == 0:
			t.Errorf("%s: no I2 breach reported", tc.name)
		case !tc.breach && len(got) > 0:
			t.Errorf("%s: legal, and I2 reports %v", tc.name, got)
		}
	}
	if end, _ := endOfAll(map[string]journal{id: {last: restoreswap.StepSwappedBack}, "r-older": {last: restoreswap.StepApplied}}); end != endMixed {
		t.Errorf("endOfAll over two different ends = %s, want %s", end, endMixed)
	}
}

// TestStateKeyIsTheContent: the nested-kill dedupe's key (stateKey) is equal
// for two filesystems with the same directories and file bytes however they
// were built, and differs for any difference in a name, a byte, an empty
// directory, or a file standing where a directory stood.
func TestStateKeyIsTheContent(t *testing.T) {
	t.Parallel()
	type entry struct{ path, body string } // body "/" is a directory
	build := func(entries ...entry) [32]byte {
		t.Helper()
		mem := vfs.NewMem()
		for _, e := range entries {
			if e.body == "/" {
				if err := mem.MkdirAll(e.path, 0o700); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := mem.MkdirAll(filepath.Dir(e.path), 0o700); err != nil {
				t.Fatal(err)
			}
			f, err := mem.Create(e.path, vfs.WriteCategoryUnspecified)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
		key, err := stateKey(mem)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	base := []entry{{"/cave/batmobile", "fuel"}, {"/cave/suits/mark-1", "cowl"}, {"/cave/gauntlet", ""}, {"/manor/attic", "/"}}
	want := build(base...)
	reversed := slices.Clone(base)
	slices.Reverse(reversed)
	if build(reversed...) != want {
		t.Errorf("the same tree built in another order has another key")
	}
	// with is base with entry i replaced by e, and more appended (a later
	// entry for one path rewrites the file).
	with := func(i int, e entry, more ...entry) []entry {
		out := slices.Clone(base)
		if i == len(out) {
			out = append(out, e)
		} else {
			out[i] = e
		}
		return append(out, more...)
	}
	for _, tc := range []struct {
		name    string
		entries []entry
	}{
		{"a byte", with(0, entry{"/cave/batmobile", "fuem"})},
		{"a name", with(1, entry{"/cave/suits/mark-2", "cowl"})},
		{"an empty directory", with(len(base), entry{"/cave/trophies", "/"})},
		{"a file for a directory", with(3, entry{"/manor/attic", ""})},
		{"a byte moved into the next file", with(0, entry{"/cave/batmobile", "fue"}, entry{"/cave/gauntlet", "l"})},
	} {
		if build(tc.entries...) == want {
			t.Errorf("%s: the key did not change", tc.name)
		}
	}
}
