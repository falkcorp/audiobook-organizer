// file: internal/decisions/retired_versions.go
// version: 1.1.1
// guid: 7c41e2a9-0d58-4b36-a1f3-5e92b6d08c74
// last-edited: 2026-10-24

package decisions

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// OpKindRetiredVersions is the op_kind of the retired-versions fixer's
// proposals (the Repairs-lane fixer `retired-versions`). An owner reject of one
// of its rows is stored under it with the SHELL (the trashed book whose file
// rows would be retired onto a live book) as the one entity.
const OpKindRetiredVersions = "repairs:retired-versions"

// retiredEntityBook is the entity type of a book.
const retiredEntityBook = "book"

// RetiredVersionsSubjectKey is the subject key of the retired-versions proposal
// for shellID: op_kind OpKindRetiredVersions, entity type "book", the shell as
// the only entity, and no scope (one proposal per shell; the op_kind already
// names the fixer). The plan's reject check (Vetoes.SubjectRejected), the
// reject write and the applied/undone records all use this one tuple, so a
// shell's reject, its apply and its undo are one History chain.
func RetiredVersionsSubjectKey(shellID string) (string, error) {
	return SubjectKey(OpKindRetiredVersions, retiredEntityBook, []string{shellID}, "")
}

// RetiredBookReader is the slice of the store RetiredBookResolver reads.
// database.Store carries GetBookByID; GetRetiredVersionsFromBook and
// GetRetiredBookFiles are on the database.RetiredBookFileReader capability
// (resolve it with database.AsCapability).
type RetiredBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error)
	GetRetiredBookFiles(holderID string) ([]database.BookFile, error)
}

// ErrRetiredShellAmbiguous is the Kind of the UnresolvableIDError
// RetiredBookResolver returns for a shell whose retired versions, each still
// holding rows, name more than one holder. It is an error, not a guess: an id
// that cannot be resolved to one book must not silently pick a side of a veto.
// The error lists every candidate holder (UnresolvableIDError.Related).
var ErrRetiredShellAmbiguous = errors.New("retired versions of one book name several holders")

// RetiredBookResolver is the IDResolver for a book that was retired: it follows
// idx:bv:from:<shell> (database.GetRetiredVersionsFromBook) to the book that
// holds the shell's files now. Version.BookID is the CURRENT holder, also after
// a RehomeRetiredVersion moved the version on to the holder's own target, so one
// hop reaches the newest holder; ChainPathResolvers then continues from there.
//
// It exists because the purge that follows a retire deletes the shell's book
// row: a veto naming a purged shell would otherwise be looked up under an id
// that no longer resolves to anything, and fail OPEN. It resolves:
//   - a live book to itself (a book restored from the trash may still have
//     version rows naming it as a shell; it is live and is not redirected);
//   - a trashed or purged book with retired versions to their holder;
//   - any other book, and any other entity type, to itself.
//
// Only a version that is retired, whose RetiredFromBookID is the shell AND that
// still holds at least one retired row counts. Reverting a retire
// (UnretireBookFiles) puts the rows back and leaves the version row in place, so
// an emptied version is a record of a retire that no longer stands: it does not
// redirect the shell, and a shell retired again onto another book is not
// ambiguous for having the old, empty version. A shell whose live versions name
// more than one holder is an UnresolvableIDError of Kind ErrRetiredShellAmbiguous.
// The resolver follows ONE hop; compose it with the merge survivor resolver
// through ChainPathResolvers (internal/merge sits at this package's layer, so
// the caller that imports both does the composing; see
// maintenance.NewOwnerVetoReader).
func RetiredBookResolver(r RetiredBookReader) IDResolver {
	return func(entityType, id string) (string, error) {
		if entityType != retiredEntityBook {
			return id, nil
		}
		b, err := r.GetBookByID(id)
		if err != nil {
			return "", fmt.Errorf("decisions: resolve retired book %s: %w", id, err)
		}
		if b != nil && !b.IsSoftDeleted() {
			return id, nil
		}
		versions, err := r.GetRetiredVersionsFromBook(id)
		if err != nil {
			return "", fmt.Errorf("decisions: resolve retired book %s: %w", id, err)
		}
		var holders []string
		// versionsHeld caches, per holder, the versions that still hold rows.
		versionsHeld := map[string]map[string]bool{}
		for i := range versions {
			v := &versions[i]
			if v.Status != database.BookVersionStatusRetired || v.RetiredFromBookID != id || v.BookID == "" || v.BookID == id {
				continue
			}
			held, ok := versionsHeld[v.BookID]
			if !ok {
				files, err := r.GetRetiredBookFiles(v.BookID)
				if err != nil {
					return "", fmt.Errorf("decisions: resolve retired book %s: read the rows retired into %s: %w", id, v.BookID, err)
				}
				held = map[string]bool{}
				for j := range files {
					if files[j].Retired {
						held[files[j].VersionID] = true
					}
				}
				versionsHeld[v.BookID] = held
			}
			if held[v.ID] && !slices.Contains(holders, v.BookID) {
				holders = append(holders, v.BookID)
			}
		}
		// Sorted, so the failure (and the row fingerprint made from it) does
		// not depend on the order the index happened to be read in.
		slices.Sort(holders)
		switch len(holders) {
		case 0:
			return id, nil
		case 1:
			return holders[0], nil
		}
		dlog.Error("book %s: retired versions name several holders (%s); refusing to resolve it",
			logger.SanitizeLogValue(id), logger.SanitizeLogValue(strings.Join(holders, ", ")))
		return "", &UnresolvableIDError{EntityType: entityType, ID: id, Kind: ErrRetiredShellAmbiguous, Related: holders}
	}
}

// Recorder is the one write the retired-versions records make. *Store
// satisfies it.
type Recorder interface {
	RecordUnlessSeen(in Input, same func(Record) bool) (*Record, error)
}

// RetiredVersionsEvent is one applied or reverted retired-versions row.
type RetiredVersionsEvent struct {
	// ShellID is the trashed book whose file rows were retired (the row id).
	ShellID string
	// TargetID is the live book that holds them (the holder).
	TargetID string
	// VersionID is the retired book_versions row the retire minted. Required:
	// it is what tells one retire of the shell from another.
	VersionID string
	// OpID is the operation that performed the event, whose journal carries it:
	// the apply op for an applied record, the revert (undo) op for an undone
	// one. Required. The apply op an undone record took back is the op_id of the
	// applied record before it on the same chain. With VersionID and the
	// decision it makes the record idempotent: a retry of the same event is the
	// same record.
	OpID string
	// PlanOpID and PlanRowID are the repairs plan op and row the retire came
	// from (provenance; the row id is the shell's).
	PlanOpID, PlanRowID string
	// ActorUserID and ActorName are who ran the apply or the revert. With no
	// actor the record's source is auto; with one it is human.
	ActorUserID, ActorName string
	ReasonText             string
}

// RecordRetiredApplied writes an `applied` record for a retired-versions row.
// `applied` is a label: it vetoes nothing and lifts no reject. It is
// idempotent: a retry of the same event (same version and op) finds the record
// already on the shell's chain and returns it instead of writing a second.
func RecordRetiredApplied(rec Recorder, ev RetiredVersionsEvent) (*Record, error) {
	return recordRetired(rec, DecisionApplied, ev)
}

// RecordRetiredUndone writes an `undone` record for a retired-versions row
// that a revert took back. `undone` is a label, not a veto (see the package
// comment): the owner may have undone it for an unrelated reason, and the shell
// stays plannable. Idempotent the same way RecordRetiredApplied is.
func RecordRetiredUndone(rec Recorder, ev RetiredVersionsEvent) (*Record, error) {
	return recordRetired(rec, DecisionUndone, ev)
}

func recordRetired(rec Recorder, d Decision, ev RetiredVersionsEvent) (*Record, error) {
	if ev.ShellID == "" {
		return nil, invalid("retired-versions %s record: shell id is empty", d)
	}
	// Without both, "the same event" cannot be told from any other: an empty
	// pair would match every earlier record that also lacks them.
	if ev.VersionID == "" || ev.OpID == "" {
		return nil, invalid("retired-versions %s record for %s: version id and op id are required", d, ev.ShellID)
	}
	src := SourceAuto
	if ev.ActorUserID != "" {
		src = SourceHuman
	}
	current := map[string]string{
		"shell_id": ev.ShellID, "target_id": ev.TargetID, "version_id": ev.VersionID, "op_id": ev.OpID,
	}
	entities := []EntitySnapshot{{EntityType: retiredEntityBook, EntityID: ev.ShellID, Label: "shell"}}
	if ev.TargetID != "" {
		entities = append(entities, EntitySnapshot{EntityType: retiredEntityBook, EntityID: ev.TargetID, Label: "target"})
	}
	// A retry (a crashed run resumed, an undo repeated) must not stack
	// duplicates on the chain. The search and the write are one hold of the
	// subject's lock (RecordUnlessSeen), so two concurrent retries write once.
	return rec.RecordUnlessSeen(Input{
		OpKind: OpKindRetiredVersions, EntityType: retiredEntityBook, EntityIDs: []string{ev.ShellID},
		PlanOpID: ev.PlanOpID, PlanRowID: ev.PlanRowID,
		Snapshot: Snapshot{Entities: entities, Current: current},
		Decision: d, Source: src, ActorUserID: ev.ActorUserID, ActorName: ev.ActorName,
		ReasonText: ev.ReasonText,
	}, func(r Record) bool {
		return r.Decision == d && r.Snapshot.Current["version_id"] == ev.VersionID && r.Snapshot.Current["op_id"] == ev.OpID
	})
}
