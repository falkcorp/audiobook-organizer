// file: internal/plugins/maintenance/kinds_t6_g5_lease_test.go
// version: 1.0.0
// guid: 7b3e5d92-1c48-4f6a-a0d7-5e92c3b1f846
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// t6g5LapseAfterRows is a scan controller whose lease is good until `limit`
// rows have been written and gone from then on. The count comes from the store
// the test hands the op, so the lapse lands BETWEEN two rows of one book: after
// the pre-write heartbeat has already passed (row 0 of the stat pass sees a good
// lease) and inside the batch write, which is the abort this file pins.
type t6g5LapseAfterRows struct {
	mu      sync.Mutex
	limit   int
	written func() int
	renews  int
}

func (c *t6g5LapseAfterRows) AcquireScanStandDown(context.Context, string, string) (func(), error) {
	return func() {}, nil
}

func (c *t6g5LapseAfterRows) RenewScanStandDown(string) bool {
	c.mu.Lock()
	c.renews++
	c.mu.Unlock()
	return c.written() < c.limit
}

func (c *t6g5LapseAfterRows) ScanStandDownValid(string) bool { return c.written() < c.limit }

const (
	t6g5LapseBookRows = 6
	t6g5LapseAfter    = 3
)

// TestT6G5MarkMissing_LeaseLapseMidBookStopsTheBatch: one book of six rows whose
// files are gone, a lease that lapses once three rows are written. The batch
// writes exactly those three, keeps them, and the run reports the abort. It is
// the in-batch abort of t6g5WriteBookFileBatch, not the pre-write heartbeat: the
// lease is still good when the stat pass checks it, and the store sees one
// UpdateBookFiles call.
func TestT6G5MarkMissing_LeaseLapseMidBookStopsTheBatch(t *testing.T) {
	dir := t.TempDir()
	store := &markFakeStore{full: map[string][]database.BookFile{}}
	for i := range t6g5LapseBookRows {
		id := fmt.Sprintf("f%d", i)
		path := filepath.Join(dir, id+".mp3") // never written, so the row flips to Missing
		store.cores = append(store.cores, database.BookFileCore{ID: id, BookID: "b1", FilePath: path})
		store.full["b1"] = append(store.full["b1"], database.BookFile{ID: id, BookID: "b1", FilePath: path})
	}
	c := &t6g5LapseAfterRows{limit: t6g5LapseAfter, written: func() int {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.updates)
	}}

	params := markMissingParams{Apply: true}
	plan, err := planMarkMissingFilesVia(context.Background(), store, store, params,
		t6g5MarkEnv(context.Background(), params.Apply, c, &opIDReporter{id: "op-lapse-mm"}))
	require.Error(t, err, "a lease lost mid-book is an error")
	assert.Contains(t, err.Error(), "lease lapsed mid-apply")
	assert.Len(t, store.updates, t6g5LapseAfter, "the batch stopped after the row that saw the lease go")
	assert.Equal(t, t6g5LapseAfter, plan.MarkedMissing, "the committed rows are counted and kept")
	assert.Equal(t, 1, store.batches, "one batched write for the book")
}

// TestT6G5Repoint_LeaseLapseMidBookStopsTheBatch is the same for repoint.
func TestT6G5Repoint_LeaseLapseMidBookStopsTheBatch(t *testing.T) {
	dir := t.TempDir()
	store := &repointFakeStore{full: map[string][]database.BookFile{}}
	for i := range t6g5LapseBookRows {
		id := fmt.Sprintf("f%d", i)
		writeFile(t, filepath.Join(dir, fmt.Sprintf("Stem - %02d.mp3", i+1)), 1000)
		broken := filepath.Join(dir, fmt.Sprintf("Stem - %d", i+1), "35.mp3")
		store.cores = append(store.cores, database.BookFileCore{ID: id, BookID: "b1", FilePath: broken, FileSize: 1000})
		store.full["b1"] = append(store.full["b1"], database.BookFile{ID: id, BookID: "b1", FilePath: broken, FileSize: 1000})
	}
	c := &t6g5LapseAfterRows{limit: t6g5LapseAfter, written: func() int {
		store.mu.Lock()
		defer store.mu.Unlock()
		return len(store.updates)
	}}

	params := missingFileRepointParams{Apply: true}
	plan, err := planMissingFileRepointVia(context.Background(), store, t6g5RepointAll(store), store, params,
		t6g5RepointEnv(context.Background(), params.Apply, c, &opIDReporter{id: "op-lapse-rp"}))
	require.Error(t, err, "a lease lost mid-book is an error")
	assert.Contains(t, err.Error(), "lease lapsed mid-apply")
	assert.Len(t, store.updates, t6g5LapseAfter, "the batch stopped after the row that saw the lease go")
	assert.Equal(t, t6g5LapseAfter, plan.Repointed, "the committed rows are counted and kept")
}

// TestT6G5WriteBookFileBatch_StampsEveryRowAndAbortsAfterN drives the batch
// write over the env directly: a liveness stamp after every row written, and an
// abort that stops the NEXT row and reports Cancelled with the committed rows
// kept. The HEAD helper writeBookFileBatch is a wrapper over the same body.
func TestT6G5WriteBookFileBatch_StampsEveryRowAndAbortsAfterN(t *testing.T) {
	store := &markFakeStore{}
	rows := make([]*database.BookFile, t6g5LapseBookRows)
	for i := range rows {
		rows[i] = &database.BookFile{ID: fmt.Sprintf("f%d", i), BookID: "b1"}
	}
	var mu sync.Mutex
	touches := 0
	env := t6g5Base{Touch: func() { mu.Lock(); touches++; mu.Unlock() }}
	seen := 0
	out := t6g5WriteBookFileBatch(context.Background(), store, rows, env, t6g5BatchOpts{
		OnRow: func(int, bool) { seen++ },
		Abort: func() bool { return seen >= t6g5LapseAfter },
	})
	assert.True(t, out.Cancelled, "an abort is reported as a cancelled batch")
	assert.Equal(t, t6g5LapseAfter, out.Applied)
	assert.Equal(t, 0, out.RowErrs)
	assert.Len(t, store.updates, t6g5LapseAfter, "rows after the abort are never attempted")
	mu.Lock()
	assert.Equal(t, t6g5LapseAfter, touches, "the run is stamped live once per written row")
	mu.Unlock()
}

// t6g5ListErrStore is a user-state store whose user listing fails.
type t6g5ListErrStore struct{ merge.UserStateRepairStore }

func (t6g5ListErrStore) ListUsers() ([]database.User, error) {
	return nil, errors.New("user listing down")
}

type t6g5ListErrDeps struct{ fakeDeps }

func (d t6g5ListErrDeps) MergeUserStateStore() merge.UserStateRepairStore {
	return t6g5ListErrStore{d.fakeDeps.MergeUserStateStore()}
}

// TestT6G5HeadWriter_NewUserStateProbeErrorIsANilInterface: a probe that cannot
// list users returns the error AND a nil probe. Returning merge.NewUserStateProbe's
// result directly would hand back a nil *UserStateProbe wrapped in a non-nil
// interface, and the regroup apply's nil check would let it through to be
// called.
func TestT6G5HeadWriter_NewUserStateProbeErrorIsANilInterface(t *testing.T) {
	s := newRepairPebble(t)
	w := headT6g5Writer{p: New(t6g5ListErrDeps{fakeDeps{store: s, root: t.TempDir()}})}
	probe, err := w.NewUserStateProbe()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list users")
	assert.True(t, probe == nil, "the failed probe is a nil interface, not a nil pointer inside one")
}

// t6g5ResLiteral matches tasks.Res("<module>", "<kind literal>").
var t6g5ResLiteral = regexp.MustCompile(`tasks\.Res\("[^"]*",\s*"`)

// TestT6G5Effects_NoResourceNameLiteral: every resource name this package
// declares is a pkg/ops constant (string(ops.ResX)), never a string literal, so
// a coined name does not compile and F5-18's writer stamps the same vocabulary.
func TestT6G5Effects_NoResourceNameLiteral(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		require.NoError(t, err)
		checked++
		for n, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			assert.False(t, t6g5ResLiteral.MatchString(line), "%s:%d declares a resource by literal name: %s", name, n+1, strings.TrimSpace(line))
		}
	}
	require.Positive(t, checked, "the guard read the package's source files")
}
