// file: internal/decisions/retired_versions.go
// version: 1.0.0
// guid: 7c41e2a9-0d58-4b36-a1f3-5e92b6d08c74
// last-edited: 2026-10-23

package decisions

import (
	"errors"
	"fmt"

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
// database.Store carries GetBookByID; GetRetiredVersionsFromBook is on the
// database.RetiredBookFileReader capability (resolve it with
// database.AsCapability).
type RetiredBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error)
}

// ErrRetiredShellAmbiguous is returned by RetiredBookResolver for a shell whose
// retired versions name more than one holder. It is an error, not a guess: an
// id that cannot be resolved to one book must not silently pick a side of a
// veto.
var ErrRetiredShellAmbiguous = errors.New("decisions: retired versions of one book name several holders")

// RetiredBookResolver is the IDResolver for a book that was retired: it follows
// idx:bv:from:<shell> (database.GetRetiredVersionsFromBook) to the book that
// holds the shell's files now. Version.BookID is the CURRENT holder, also after
// a RehomeRetiredVersion moved the version on to the holder's own target, so one
// hop reaches the newest holder; ChainResolvers then continues from there.
//
// It exists because the purge that follows a retire deletes the shell's book
// row: a veto naming a purged shell would otherwise be looked up under an id
// that no longer resolves to anything, and fail OPEN. It resolves:
//   - a live book to itself (a book restored from the trash may still have
//     version rows naming it as a shell; it is live and is not redirected);
//   - a trashed or purged book with retired versions to their holder;
//   - any other book, and any other entity type, to itself.
//
// Only versions of status retired whose RetiredFromBookID is the shell count. A
// shell whose versions name more than one holder is ErrRetiredShellAmbiguous.
// It follows ONE hop; compose it with the merge survivor resolver through
// ChainResolvers (internal/merge sits at this package's layer, so the caller
// that imports both does the composing; see maintenance.NewOwnerVetoReader).
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
		holder := ""
		for i := range versions {
			v := &versions[i]
			if v.Status != database.BookVersionStatusRetired || v.RetiredFromBookID != id || v.BookID == "" || v.BookID == id {
				continue
			}
			if holder != "" && holder != v.BookID {
				dlog.Error("book %s: retired versions name several holders (%s, %s); refusing to resolve it",
					logger.SanitizeLogValue(id), logger.SanitizeLogValue(holder), logger.SanitizeLogValue(v.BookID))
				return "", fmt.Errorf("%w: book %s names %s and %s", ErrRetiredShellAmbiguous, id, holder, v.BookID)
			}
			holder = v.BookID
		}
		if holder == "" {
			return id, nil
		}
		return holder, nil
	}
}

// ChainResolvers composes resolvers into one that applies them in order, over
// and over, until none moves the id (a fixed point). That is what a book needs:
// a purged shell resolves to its retired-version holder, which may itself have
// been merged away, which resolves to its survivor. A chain longer than
// maxTombstoneHops (or one that loops) is an error, never a partial answer.
func ChainResolvers(resolvers ...IDResolver) IDResolver {
	return func(entityType, id string) (string, error) {
		cur := id
		for hop := 0; hop < maxTombstoneHops; hop++ {
			moved := false
			for _, r := range resolvers {
				next, err := r(entityType, cur)
				if err != nil {
					return "", err
				}
				if next != cur {
					cur, moved = next, true
				}
			}
			if !moved {
				return cur, nil
			}
		}
		dlog.Error("%s %s: resolver chain longer than %d hops (cycle?); refusing to resolve it",
			logger.SanitizeLogValue(entityType), logger.SanitizeLogValue(id), maxTombstoneHops)
		return "", fmt.Errorf("decisions: %s %s: resolver chain longer than %d hops", entityType, id, maxTombstoneHops)
	}
}

// Recorder is the one write the retired-versions records make. *Store
// satisfies it.
type Recorder interface {
	Record(in Input) (*Record, error)
	Verdict(opKind, subjectKey string) (*Verdict, error)
	Get(id string) (*Record, error)
}

// RetiredVersionsEvent is one applied or reverted retired-versions row.
type RetiredVersionsEvent struct {
	// ShellID is the trashed book whose file rows were retired (the row id).
	ShellID string
	// TargetID is the live book that holds them (the holder).
	TargetID string
	// VersionID is the retired book_versions row the retire minted.
	VersionID string
	// OpID is the operation whose journal carries the retire: the apply op for
	// an applied record, the apply op that was reverted for an undone one.
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
// idempotent: a retry that finds the shell's latest record to be an applied one
// for the same version returns that record instead of writing a second.
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
	key, err := RetiredVersionsSubjectKey(ev.ShellID)
	if err != nil {
		return nil, err
	}
	// A retry (a crashed run resumed, an undo repeated) must not stack
	// duplicates on the chain: stop at an identical latest record.
	if v, err := rec.Verdict(OpKindRetiredVersions, key); err != nil {
		return nil, err
	} else if v != nil && v.LatestID != "" {
		last, err := rec.Get(v.LatestID)
		if err != nil {
			return nil, err
		}
		if last.Decision == d && last.Snapshot.Current["version_id"] == ev.VersionID && last.Snapshot.Current["op_id"] == ev.OpID {
			return last, nil
		}
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
	return rec.Record(Input{
		OpKind: OpKindRetiredVersions, EntityType: retiredEntityBook, EntityIDs: []string{ev.ShellID},
		PlanOpID: ev.PlanOpID, PlanRowID: ev.PlanRowID,
		Snapshot: Snapshot{Entities: entities, Current: current},
		Decision: d, Source: src, ActorUserID: ev.ActorUserID, ActorName: ev.ActorName,
		ReasonText: ev.ReasonText,
	})
}
