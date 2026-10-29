// file: internal/repairs/writer_surface_test.go
// version: 1.2.2
// guid: 0f69b17c-331b-4c43-9317-c7276540fe70
// last-edited: 2026-10-29

package repairs

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// repairs.Writer became a wrapper over internal/operations/opswriter in
// 05-PR5. Every fixer and the engine call it, so the wrapper must keep the
// exact method set it had, signatures included. Each line below is a method
// expression assigned to its full signature: a renamed, removed or re-typed
// method fails to compile here.
var (
	_ func(*Writer, string, string, string) (bool, error)                                                                = (*Writer).AddBookTag
	_ func(*Writer, string) error                                                                                        = (*Writer).Beat
	_ func(*Writer, string, string, string) error                                                                        = (*Writer).ClearBookFileITunesPath
	_ func(*Writer, string, string) error                                                                                = (*Writer).ClearBookITunesPath
	_ func(*Writer) int                                                                                                  = (*Writer).HistoryFailed
	_ func(*Writer) int                                                                                                  = (*Writer).HistoryRows
	_ func(*Writer, string, string, string, string, string) error                                                        = (*Writer).Journal
	_ func(*Writer, string, UndoEntry, func() error) error                                                               = (*Writer).JournalStep
	_ func(*Writer, []BookStep, func() ([]bool, error)) error                                                            = (*Writer).JournalSteps
	_ func(*Writer) bool                                                                                                 = (*Writer).JournalsInBatches
	_ func(*Writer) int                                                                                                  = (*Writer).Journaled
	_ func(*Writer, string, string, string) (string, bool, error)                                                        = (*Writer).JournaledValue
	_ func(*Writer, string, ...string) error                                                                             = (*Writer).LockFields
	_ func(*Writer, context.Context, string, func(), func()) error                                                       = (*Writer).LockWaiting
	_ func(*Writer, string, func(*database.Book) error) ([]string, error)                                                = (*Writer).Modify
	_ func(*Writer, []string, string, string) error                                                                      = (*Writer).MoveBookFiles
	_ func(*Writer) string                                                                                               = (*Writer).OpID
	_ func(*Writer) ApplyProvenance                                                                                      = (*Writer).Provenance
	_ func(*Writer, string) error                                                                                        = (*Writer).Recompute
	_ func(*Writer, string, UndoEntry) error                                                                             = (*Writer).RecordChange
	_ func(*Writer, string, string, database.BookFileLocation, database.BookFileLocation) error                          = (*Writer).RepointBookFile
	_ func(*Writer, string, string) error                                                                                = (*Writer).RehomeRetiredVersion
	_ func(*Writer, RetireSpec) error                                                                                    = (*Writer).RetireVersion
	_ func(*Writer, string, int, *database.Author) error                                                                 = (*Writer).SetPrimaryAuthor
	_ func(*Writer, string, string, int, int) error                                                                      = (*Writer).SetTrackNumber
	_ func(*Writer, context.Context, string, string, undo.UserStateSnapshot, undo.UserStateSnapshot) error               = (*Writer).SetUserState
	_ func(*Writer, context.Context, string, string, undo.UserStateSnapshot, undo.UserStateSnapshot, func() error) error = (*Writer).SetUserStateChecked
	_ func(*Writer, string, string, string, string, string, func() error) error                                          = (*Writer).Step
	_ func(*Writer, string, []string) error                                                                              = (*Writer).SupersedeChanges
	_ func(*Writer)                                                                                                      = (*Writer).Touch
	_ func(*Writer, CreditStore) *Writer                                                                                 = (*Writer).WithCredits
	_ func(*Writer, FieldStateStore) *Writer                                                                             = (*Writer).WithFieldStates
	_ func(*Writer, BookFileWriter, ChangeJournal, string) *Writer                                                       = (*Writer).WithJournal
	_ func(*Writer, func()) *Writer                                                                                      = (*Writer).WithLiveness
	_ func(*Writer, TagStore) *Writer                                                                                    = (*Writer).WithTags
	_ func(*Writer, UserStateStore) *Writer                                                                              = (*Writer).WithUserState
	_ func(*Writer) int                                                                                                  = (*Writer).Writes

	_ func(*Writer, string, func([]database.BookAuthor) ([]database.BookAuthor, UndoEntry, error)) ([]database.BookAuthor, error) = (*Writer).ModifyCredits
)

// TestWriterMethodSetPinned fails when a method is added to or disappears
// from the exported surface; the block above fails to compile when one
// changes its signature.
func TestWriterMethodSetPinned(t *testing.T) {
	want := []string{"AddBookTag", "Beat", "ClearBookFileITunesPath", "ClearBookITunesPath", "HistoryFailed",
		"HistoryRows", "Journal", "JournalStep", "JournalSteps", "JournalsInBatches", "Journaled", "JournaledValue", "LockFields",
		"LockWaiting", "Modify", "ModifyCredits", "MoveBookFiles", "OpID", "Provenance", "Recompute", "RecordChange",
		"RehomeRetiredVersion", "RepointBookFile", "RetireVersion", "SetPrimaryAuthor", "SetTrackNumber", "SetUserState", "SetUserStateChecked", "Step", "SupersedeChanges", "Touch", "WithCredits",
		"WithFieldStates", "WithJournal", "WithLiveness", "WithTags", "WithUserState", "Writes"}
	typ := reflect.TypeFor[*Writer]()
	got := make([]string, 0, typ.NumMethod())
	for method := range typ.Methods() {
		got = append(got, method.Name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("repairs.Writer's method set changed:\n got  %v\n want %v", got, want)
	}
}
