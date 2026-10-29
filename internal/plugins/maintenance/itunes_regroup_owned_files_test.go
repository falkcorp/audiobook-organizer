// file: internal/plugins/maintenance/itunes_regroup_owned_files_test.go
// version: 1.1.0
// guid: c3a58e17-4f20-4b96-8d71-e0b92a6f5c34
// last-edited: 2026-10-29

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

// retireRowInto gives holderID one retired book_file row, re-keyed from a
// shell book into it, so the holder owns a retired row besides whatever else it
// has. GetBookFiles does not list the row; DeleteBook counts it.
func retireRowInto(t *testing.T, s *database.PebbleStore, holderID string) {
	t.Helper()
	shell := seedBook(t, s, "Arkham Shell")
	row := &database.BookFile{ID: ulid.Make().String(), BookID: shell, FilePath: "/x/retired-" + holderID + ".m4b", Format: "m4b"}
	if err := s.CreateBookFile(row); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	v, err := s.CreateBookVersion(&database.BookVersion{
		BookID: holderID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell, RetireOpID: "op-regroup-test", RetiredAt: &at,
	})
	if err != nil {
		t.Fatalf("CreateBookVersion: %v", err)
	}
	if err := s.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell}}, holderID, v.ID); err != nil {
		t.Fatalf("RetireBookFiles: %v", err)
	}
}

// regroupLoserWithState is regroupWithState with the loser's progress changed
// to 77% (the survivor keeps 30%), so a carry of the loser's state onto the
// survivor is visible as the state leaving the loser.
func regroupLoserWithState(t *testing.T) (s *database.PebbleStore, userID, loser string, plan itunesservice.RegroupPlan, rep *fakeReporter) {
	t.Helper()
	s, u, plan, rep := regroupWithState(t)
	loser = plan.DeleteBooks[0]
	if err := s.SetUserBookState(&database.UserBookState{UserID: u.ID, BookID: loser, Status: database.UserBookStatusInProgress, ProgressPct: 77}); err != nil {
		t.Fatalf("SetUserBookState: %v", err)
	}
	return s, u.ID, loser, plan, rep
}

func requireLoserKeptWithItsState(t *testing.T, s *database.PebbleStore, userID, loser string) {
	t.Helper()
	if b, _ := s.GetBookByID(loser); b == nil {
		t.Fatalf("book %s was deleted although it holds a retired version", loser)
	}
	st, err := s.GetUserBookState(userID, loser)
	if err != nil || st == nil || st.ProgressPct != 77 {
		t.Fatalf("loser state = %+v (err=%v), want the 77%% still on it: the carry ran before the delete was refused", st, err)
	}
}

// A projected-empty book that holds ONLY a retired row reads as empty through
// GetBookFiles. The delete pass used to carry its users' listening state onto
// the regrouped book and then have DeleteBook refuse it (which counts every
// row key), leaving the book standing without its state. It is now kept with
// the state on it, and the skip says why.
func TestITunesRegroupApply_DeleteKeepsHolderOfOnlyRetiredRowsWithItsState(t *testing.T) {
	s, userID, loser, plan, rep := regroupLoserWithState(t)
	retireRowInto(t, s, loser)

	p := &Plugin{}
	if _, err := p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}
	live, err := s.GetBookFiles(loser)
	if err != nil || len(live) != 0 {
		t.Fatalf("fixture: loser has %d live rows (err=%v), want 0 so that only the retired row is left", len(live), err)
	}
	requireLoserKeptWithItsState(t, s, userID, loser)
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "skip delete "+loser) || !strings.Contains(logs, "retired") || !strings.Contains(logs, "delete-skipped=1") {
		t.Fatalf("the skip is not reported with its reason: %s", logs)
	}
	if strings.Contains(logs, "re-checked under the merge lock") {
		t.Fatalf("the pass's own check missed the retired row and left it to the check under the lock: %s", logs)
	}
}

// retiredAfterFirstReadStore gives bookID a retired row right after the first
// time its files are read including retired ones: the delete pass's own check
// has passed by then, and a retire (which takes the merge lock the carry runs
// under) lands before the lock.
type retiredAfterFirstReadStore struct {
	*database.PebbleStore
	t      *testing.T
	bookID string

	mu    sync.Mutex
	calls int
}

func (r *retiredAfterFirstReadStore) GetBookFilesIncludingRetired(id string) ([]database.BookFile, error) {
	rows, err := r.PebbleStore.GetBookFilesIncludingRetired(id)
	if id == r.bookID {
		r.mu.Lock()
		r.calls++
		first := r.calls == 1
		r.mu.Unlock()
		if first {
			retireRowInto(r.t, r.PebbleStore, id)
		}
	}
	return rows, err
}

// The same check is made again under the merge lock, before the carry moves
// anything: a retired row that lands after the pass's own check is caught
// there and nothing is carried.
func TestITunesRegroupApply_DeleteRechecksOwnedRowsUnderTheLockBeforeTheCarry(t *testing.T) {
	s, userID, loser, plan, rep := regroupLoserWithState(t)
	w := &retiredAfterFirstReadStore{PebbleStore: s, t: t, bookID: loser}

	p := &Plugin{}
	if _, err := p.applyRegroupPlan(context.Background(), w, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}
	if w.calls < 2 {
		t.Fatalf("the loser's rows were read %d time(s), want at least 2: the check under the merge lock never ran", w.calls)
	}
	requireLoserKeptWithItsState(t, s, userID, loser)
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "re-checked under the merge lock and nothing was carried") {
		t.Fatalf("the under-lock refusal is not reported: %s", logs)
	}
}

// The production run store wraps the server's store in a struct that embeds the
// narrow OpsStore interface, which hides every capability outside it. The
// owned-files check needs the retired-row reader, so the constructor
// runITunesRegroup builds the run store with composes it in: without that the
// check fails closed on every book and the apply deletes nothing.
func TestRegroupRunStore_ExposesTheRetiredRowReader(t *testing.T) {
	s := regroupStore(t)
	rs, err := newRegroupRunStore(s, s)
	if err != nil {
		t.Fatalf("newRegroupRunStore: %v", err)
	}
	if _, ok := database.AsCapability[database.RetiredBookFileReader](rs); !ok {
		t.Fatal("the regroup run store is not a retired-row reader")
	}
	id := seedBook(t, s, "Arkham Holder")
	if err := database.CheckBookOwnsNoFiles(rs, id, "regroup delete"); err != nil {
		t.Fatalf("a book with no rows is refused through the run store: %v", err)
	}
	retireRowInto(t, s, id)
	if err := database.CheckBookOwnsNoFiles(rs, id, "regroup delete"); !errors.Is(err, database.ErrBookOwnsFiles) {
		t.Fatalf("a holder of a retired row = %v through the run store, want ErrBookOwnsFiles", err)
	}
}

// Neither reader may be missing: a nil tag reader or a store that cannot list
// retired rows is refused when the run store is built.
func TestNewRegroupRunStore_RefusesAMissingReader(t *testing.T) {
	s := regroupStore(t)
	if _, err := newRegroupRunStore(s, nil); err == nil {
		t.Error("a nil tag reader was accepted")
	}
	if _, err := newRegroupRunStore(storeHidingRetiredRows{s}, s); err == nil {
		t.Error("a store that cannot list retired rows was accepted")
	}
}

// storeHidingRetiredRows is a store whose method set is database.Store's alone:
// the retired-row reader is a capability outside it, so a decorator like this
// one (no Unwrap) does not expose it.
type storeHidingRetiredRows struct{ database.Store }

// The delete pass cannot check a book it cannot list retired rows of, so the
// run refuses up front, naming why, rather than deleting on a check it could
// not make (and rather than panicking on a nil reader). Driven through the real
// entry, which builds the run store.
func TestITunesRegroup_RefusesWhenTheStoreCannotListRetiredRows(t *testing.T) {
	s := regroupStore(t)
	xmlPath := filepath.Join(t.TempDir(), "lib.xml")
	if err := os.WriteFile(xmlPath, []byte(regroupDeltaXML), 0o600); err != nil {
		t.Fatalf("write xml: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"xmlPath": xmlPath, "dry_run": false})
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = rgRoot
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })

	p := New(fakeDeps{store: storeHidingRetiredRows{s}})
	err := p.runITunesRegroup(context.Background(), raw, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "including retired ones") {
		t.Fatalf("runITunesRegroup = %v, want a refusal naming the missing retired-row reader", err)
	}

	p = New(fakeDeps{store: s})
	if err := p.runITunesRegroup(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("the same run on a store that can list retired rows: %v", err)
	}
}
