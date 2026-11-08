// file: core/coretest/crashmatrix/kit_test.go
// version: 1.0.0
// guid: 67d91a53-bcd6-4094-acf4-c145ba475897
// last-edited: 2026-11-08

package crashmatrix

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
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

// TestUncoveredNamesRowsNoLabelReaches: a row counts as exercised when one
// of its labels is observed (TestBootCrashMatrixCoversEveryWriter's input).
func TestUncoveredNamesRowsNoLabelReaches(t *testing.T) {
	t.Parallel()
	rows := []Row{
		{Writer: "batcave", Labels: []string{"cave.mkdir", "cave.sync"}},
		{Writer: "wayne manor", Labels: []string{"manor.write"}},
	}
	got := Uncovered(rows, map[string]bool{"cave.sync": true})
	if len(got) != 1 || got[0].Writer != "wayne manor" {
		t.Fatalf("Uncovered = %+v, want only wayne manor", got)
	}
	if got := Uncovered(rows, map[string]bool{"cave.mkdir": true, "manor.write": true}); len(got) != 0 {
		t.Fatalf("Uncovered = %+v, want none", got)
	}
}
