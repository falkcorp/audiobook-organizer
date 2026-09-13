// file: internal/plugins/maintenance/book_atpath_index_test.go
// version: 1.0.0
// guid: 1b7e4c92-5a3d-4e68-9f02-c6d8a1b3e5f7
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type fakeAtPathVerifier struct {
	rep database.BookAtPathIndexReport
	err error
}

func (f fakeAtPathVerifier) VerifyBookAtPathIndex(context.Context) (database.BookAtPathIndexReport, error) {
	return f.rep, f.err
}

func TestBookAtPathVerify_CleanIndexPasses(t *testing.T) {
	rep := database.BookAtPathIndexReport{SentinelSet: true, BooksScanned: 3, IndexKeysScanned: 4, ExtraRowGone: 1}
	if err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{}); err != nil {
		t.Fatalf("extras alone must not fail the op: %v", err)
	}
}

func TestBookAtPathVerify_MissingLiveFails(t *testing.T) {
	rep := database.BookAtPathIndexReport{MissingLive: 2}
	err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "INCOMPLETE") {
		t.Fatalf("missing live keys must fail the op, got %v", err)
	}
}

func TestBookAtPathVerify_MissingTrashedDoesNotFail(t *testing.T) {
	rep := database.BookAtPathIndexReport{MissingTrashed: 5}
	if err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{}); err != nil {
		t.Fatalf("a trashed-book gap cannot cause a false free and must not fail: %v", err)
	}
}

func TestBookAtPathVerify_UndecodableRowsFail(t *testing.T) {
	rep := database.BookAtPathIndexReport{UndecodableRows: 1}
	if err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{rep: rep}, &fakeReporter{}); err == nil {
		t.Fatal("an unchecked row must not be reported as a passing verify")
	}
}

func TestBookAtPathVerify_StoreErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	err := verifyBookAtPathIndex(context.Background(), fakeAtPathVerifier{err: boom}, &fakeReporter{})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want wrapped boom", err)
	}
}

// The real store must satisfy both capability interfaces, or the ops refuse to
// run in production.
func TestBookAtPathOps_PebbleStoreSatisfiesCapabilities(t *testing.T) {
	var _ bookAtPathVerifier = (*database.PebbleStore)(nil)
	var _ bookAtPathRebuilder = (*database.PebbleStore)(nil)
}
