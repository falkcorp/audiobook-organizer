// file: internal/plugins/maintenance/deps_t6g4_test.go
// version: 0.1.0
// guid: 94d1a7c3-5e28-4b60-a3f9-17c0e5b8d264
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer/writertest"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
)

// g4HostSpy is the server side of the group, with no provenance keyspace and no
// AI parser wired.
type g4HostSpy struct{ root string }

func (h g4HostSpy) RootDir() string                                   { return h.root }
func (h g4HostSpy) OperationQueueStore() OpQueueReader                { return nil }
func (h g4HostSpy) HasDedupEngine() bool                              { return false }
func (h g4HostSpy) HasAIParsing() bool                                { return false }
func (h g4HostSpy) FileProvenanceStore() database.FileProvenanceStore { return nil }
func (h g4HostSpy) AIBatchSubmitter() t6g4AIBatchSubmitter            { return nil }
func (h g4HostSpy) DedupTriageExactPending(context.Context, bool) (*TriageReport, error) {
	return &TriageReport{}, nil
}

// g4WriterCalls is one call of every method of t6g4Writer, by method name.
func g4WriterCalls(bookID string) map[string]func(w t6g4Writer) {
	ctx := context.Background()
	noop := func(*database.BookFile) error { return nil }
	return map[string]func(w t6g4Writer){
		"AppendFileEvent":             func(w t6g4Writer) { _ = w.AppendFileEvent(database.FileEvent{}) },
		"CarryOverFingerprintWindows": func(w t6g4Writer) { _, _ = w.CarryOverFingerprintWindows(nil, database.FingerprintWindowRef("")) },
		"ClearBookFileIntro":          func(w t6g4Writer) { _, _, _ = w.ClearBookFileIntro(bookID, "f", "op") },
		"ClearIntroTimeouts":          func(w t6g4Writer) { _, _ = w.ClearIntroTimeouts(bookID, "f", "h") },
		"CreateBookFile":              func(w t6g4Writer) { _ = w.CreateBookFile(&database.BookFile{BookID: bookID, FilePath: "/lib/y.m4b"}) },
		"CreateOperationChange": func(w t6g4Writer) {
			_ = w.CreateOperationChange(&database.OperationChange{OperationID: "op", BookID: bookID, ChangeType: "metadata_update"})
		},
		"DedupTriageApply":     func(w t6g4Writer) { _, _ = w.DedupTriageApply(ctx) },
		"DeleteBookFilesByIDs": func(w t6g4Writer) { _, _ = w.DeleteBookFilesByIDs(nil) },
		"MarkOperationChangesReverted": func(w t6g4Writer) {
			_ = w.MarkOperationChangesReverted("op", nil)
		},
		"ModifyBook": func(w t6g4Writer) {
			_, _ = w.ModifyBook(bookID, func(*database.Book) error { return nil })
		},
		"ModifyBookFileDeferAggregates":    func(w t6g4Writer) { _, _ = w.ModifyBookFileDeferAggregates(bookID, "f", noop) },
		"ModifyBookFileReportingRecompute": func(w t6g4Writer) { _, _ = w.ModifyBookFileReportingRecompute(bookID, "f", noop) },
		"MoveBookFilesToBookBulk":          func(w t6g4Writer) { _ = w.MoveBookFilesToBookBulk(nil, bookID) },
		"OpenWhisperJournal":               func(w t6g4Writer) { _, _ = w.OpenWhisperJournal() },
		"ReassignExternalID":               func(w t6g4Writer) { _ = w.ReassignExternalID("src", "x", bookID) },
		"RebuildIdentIndex":                func(w t6g4Writer) { _, _ = w.RebuildIdentIndex(ctx) },
		"RecomputeBookAggregates":          func(w t6g4Writer) { _ = w.RecomputeBookAggregates(bookID) },
		"RecordIntroTimeout": func(w t6g4Writer) {
			_, _ = w.RecordIntroTimeout(bookID, "f", database.IntroTimeoutStrike{})
		},
		"SetBookFileHash":         func(w t6g4Writer) { _ = w.SetBookFileHash("f", "h") },
		"SetBookFileIntro":        func(w t6g4Writer) { _, _ = w.SetBookFileIntro(bookID, "f", database.BookFileIntroResult{}) },
		"SetFileProvExportCursor": func(w t6g4Writer) { _ = w.SetFileProvExportCursor(1) },
		"SubmitAuthorDedupJob": func(w t6g4Writer) {
			_, _ = w.SubmitAuthorDedupJob(ctx, "src", []ai.AuthorDiscoveryInput{})
		},
		"UpdateBookFile":  func(w t6g4Writer) { _ = w.UpdateBookFile("f", &database.BookFile{}) },
		"UpdateBookFiles": func(w t6g4Writer) { _, _ = w.UpdateBookFiles(ctx, nil, nil) },
	}
}

var _ transcribe.ResultJournal // OpenWhisperJournal's result type

func g4ByKind(t *testing.T, deps t6g4Deps, kind tasks.Kind) tasks.Definition {
	t.Helper()
	return t6g4KindByName(t, t6g4KindsUnchecked(deps, nil), kind)
}

func TestBooksT6G4Writer_CoversEveryMethodOfTheInterface(t *testing.T) {
	want := t6WriterMethods(reflect.TypeFor[t6g4Writer]())
	var got []string
	for m := range g4WriterCalls("x") {
		got = append(got, m)
	}
	slices.Sort(got)
	require.Equal(t, want, got)
}

// TestBooksT6G4Deps_NilStatsStaysNilSoRegistrationStillRefuses: the builder does
// not wrap a missing store in a Stats adapter, so t6g4Kinds's refusal of a nil
// Stats (TestT6G4Kinds_RegistrationRefusesNilStats) still fires through the
// builder, and a built one registers every kind.
func TestBooksT6G4Deps_NilStatsStaysNilSoRegistrationStillRefuses(t *testing.T) {
	s, _ := g2Fixture(t)

	d := newT6G4Deps(nil, g4HostSpy{}, nil)
	require.Nil(t, d.Stats)
	require.Nil(t, d.Store)
	require.Nil(t, d.NewWriter)
	defs, err := t6g4Kinds(d, nil)
	require.ErrorContains(t, err, "Stats")
	require.Nil(t, defs)

	d = newT6G4Deps(s, nil, nil)
	require.NotNil(t, d.Stats)
	require.Nil(t, d.Store, "no host: the reads that need it have nowhere to go")
	require.Nil(t, d.NewWriter)

	d = newT6G4Deps(s, g4HostSpy{}, nil)
	defs, err = t6g4Kinds(d, nil)
	require.NoError(t, err)
	require.Len(t, defs, len(t6g4Expected))
}

// TestBooksT6G4Writer_EveryKindsCallsAreAdmittedByItsOwnEffects: every writer
// method a kind's core can call runs through the builder's writer over the
// Effects-enforcing writer built from the kind's own definition, Live and Preview.
func TestBooksT6G4Writer_EveryKindsCallsAreAdmittedByItsOwnEffects(t *testing.T) {
	s, id := g2Fixture(t)
	deps := newT6G4Deps(s, g4HostSpy{root: t.TempDir()}, nil)
	calls := g4WriterCalls(id)
	for _, want := range t6g4Expected {
		if len(want.calls) == 0 {
			continue
		}
		def := g4ByKind(t, deps, want.kind)
		for _, mode := range []tasks.Mode{tasks.ModeLive, tasks.ModePreview} {
			t.Run(string(want.kind)+"/"+mode.String(), func(t *testing.T) {
				enforcing := writertest.New(def, mode)
				w := deps.NewWriter(enforcing)
				for _, m := range want.calls {
					calls[m](w)
				}
				require.NoError(t, enforcing.RequireClean(), "a stamp is wider than the kind's declared Effects")
				require.Len(t, enforcing.Writes(), len(want.calls), "every method is one stamped write")
			})
		}
	}
}

func TestBooksT6G4Writer_NoWriterRefusesEveryWrite(t *testing.T) {
	s, id := g2Fixture(t)
	w := newT6G4Deps(s, g4HostSpy{}, nil).NewWriter(nil)
	require.ErrorIs(t, w.RecomputeBookAggregates(id), tasks.ErrOutsideEffects)
	_, err := w.DedupTriageApply(context.Background())
	require.ErrorIs(t, err, tasks.ErrOutsideEffects)
}
