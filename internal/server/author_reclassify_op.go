// file: internal/server/author_reclassify_op.go
// version: 1.1.0
// guid: 40643c5c-2000-4271-813a-297ab2aa23e1
// last-edited: 2026-11-18

// author_reclassify_op registers entities.author-reclassify: the op behind the
// Authors view's "reclassify as narrator" button (POST
// /authors/:id/reclassify-as-narrator). The route used to run the move inline with
// no operation to journal under, so it deleted the author and moved its credits
// with no way back. Run as an op it journals every write under the op's own id
// (entities.ReclassifyJournal, change types author_reclassify_credits and
// author_reclassify_delete), and the existing revert path undoes it.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// authorReclassifyOpParams is the wire shape, defined once in package entities.
type authorReclassifyOpParams = entities.AuthorReclassifyOpParams

// authorReclassifyDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) authorReclassifyDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              entities.AuthorReclassifyOpID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "entities",
		DisplayName:     "Author Reclassify as Narrator",
		Description:     "Turn an author into a narrator of the same name: every book credited to the author gains a narrator credit and loses the author credit, and the author record is deleted. Undoable.",
		DefaultPriority: opsregistry.PriorityNormal,
		Isolate:         false,
		// Interactive: a person pressed the button and is waiting on it.
		NoWarmupWait: true,
		Timeout:      time.Hour,
		// RESUME AUDIT 2026-10-30: ResumeDrop, as entities.author-merge. A re-run after
		// a restart would find the author gone (a clean 404-shaped failure) or half
		// moved, and the journal would get a second set of rows under the same op id;
		// the interrupted run is surfaced for the person to re-issue deliberately.
		ResumePolicy: opsregistry.ResumeDrop,
		// The SAME key as entities.author-merge and ai.author-scan-apply: all three
		// rewrite book_authors and delete authors, and the registry serializes runs
		// that share a key, so a reclassify cannot interleave with a merge on the
		// same rows.
		ConcurrencyKey: "entities.author-merge",
		Permissions:    []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:   []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p authorReclassifyOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("author-reclassify: decode params: %w", err)
				}
			}
			progress := registryProgressAdapter{r: reporter}
			opID := opsregistry.ReporterOpID(reporter)
			msg, err := s.runAuthorReclassify(ctx, opID, p.AuthorID)
			if err != nil {
				_ = progress.Log("error", err.Error(), nil)
				return err
			}
			_ = progress.Log("info", msg, nil)
			return nil
		},
	}
}

// RegisterAuthorReclassifyOp registers the "entities.author-reclassify" v2 OperationDef.
func (s *Server) RegisterAuthorReclassifyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.authorReclassifyDef())
}

// authorReclassifyStore is the reads of the reclassify: the author and the
// narrator it becomes, each book's credits, and the books the author credits
// (the trash included).
type authorReclassifyStore interface {
	GetAuthorByID(id int) (*database.Author, error)
	GetAuthorByName(name string) (*database.Author, error)
	GetNarratorByName(name string) (*database.Narrator, error)
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error)
	GetAuthorAliases(authorID int) ([]database.AuthorAlias, error)
}

// authorReclassifyWriter is the writes of the reclassify: the credits it moves,
// the author it deletes, the narrator and unknown-author rows it may create, the
// undo journal row under the op's id, and the two caches the lists are served
// from.
type authorReclassifyWriter interface {
	CreateAuthor(name string) (*database.Author, error)
	DeleteAuthor(id int) error
	CreateNarrator(name string) (*database.Narrator, error)
	SetBookAuthors(bookID string, authors []database.BookAuthor) error
	SetBookNarrators(bookID string, narrators []database.BookNarrator) error
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
	CreateOperationChange(change *database.OperationChange) error
	// InvalidateAuthorCaches drops the author-duplicates and authors list caches.
	InvalidateAuthorCaches()
}

// authorReclassifyEnv is what the shared core takes from its shell (rules R2,
// R10).
type authorReclassifyEnv struct {
	t7Env
	Store     authorReclassifyStore
	Writer    authorReclassifyWriter
	StandDown t7StandDown
}

// authorReclassifyPair is the entities.ReclassifyStore (and journal store) the
// shared core hands the move: reads from the env's Store, writes from its Writer.
type authorReclassifyPair struct {
	authorReclassifyStore
	authorReclassifyWriter
}

// authorReclassifyCore is the op's body: hold the scan stand-down, reclassify the
// author journaled under env.OpID, and drop the author caches on EVERY exit after
// the first write (a partial move changes the lists too). It returns the success
// line. Shared by HEAD's shell and the native kind.
func authorReclassifyCore(ctx context.Context, authorID int, env authorReclassifyEnv) (string, error) {
	if authorID <= 0 {
		return "", fmt.Errorf("author-reclassify: invalid author id %d", authorID)
	}
	// Taken before the first write, so a refusal leaves nothing to invalidate.
	hold, herr := env.StandDown(ctx, "author-reclassify")
	if herr != nil {
		return "", fmt.Errorf("author-reclassify: %w", herr)
	}
	defer func() { _ = hold.Finish(nil) }()

	store := authorReclassifyPair{env.Store, env.Writer}
	defer env.Writer.InvalidateAuthorCaches()
	out, err := entities.ReclassifyAuthorAsNarrator(store, authorID, entities.ReclassifyOptions{
		// Journaled under this operation, so a revert of it undoes the reclassify.
		Journal: &entities.ReclassifyJournal{OperationID: env.OpID, Store: store},
		// No SkipBook: this is a person's explicit action on one author, unlike the
		// AI apply, which leaves a book whose author_name or narrator is locked.
		// Lease renews the stand-down before each book's writes.
		Lease: hold.Renew,
	})
	if err != nil {
		return "", reclassifyFailure(authorID, out, err)
	}
	return fmt.Sprintf("Reclassified author %d as narrator %d: %d book(s) updated", authorID, out.NarratorID, out.BooksUpdated), nil
}

// headAuthorReclassifyWriter is HEAD's write side: the ops store's own methods,
// and the server's two caches, each called as HEAD's body called them.
type headAuthorReclassifyWriter struct {
	database.Store
	s *Server
}

func (w headAuthorReclassifyWriter) InvalidateAuthorCaches() {
	w.s.dedupCache.Invalidate("author-duplicates")
	w.s.authorsCache.InvalidateAll()
}

// runAuthorReclassify is HEAD's body: authorReclassifyCore over the server's ops
// store, the server's caches and the scan gate's stand-down.
func (s *Server) runAuthorReclassify(ctx context.Context, opID string, authorID int) (string, error) {
	store := s.storeForWiring()
	env := authorReclassifyEnv{
		t7Env:  t7Env{Live: true, OpID: opID},
		Store:  store,
		Writer: headAuthorReclassifyWriter{Store: store, s: s},
		StandDown: func(ctx context.Context, reason string) (t7Hold, error) {
			release, err := s.holdReclassifyStandDown(ctx, opID, reason)
			if err != nil {
				return t7Hold{}, err
			}
			return t7Hold{
				Renew:  func() bool { return s.scanGate().RenewScanStandDown(opID) },
				Finish: func(runErr error) error { release(); return runErr },
			}, nil
		},
	}
	return authorReclassifyCore(ctx, authorID, env)
}

// authorReclassifyNative is the native books.entities.author-reclassify kind. It
// moves book credits (authors), creates the narrator, and deletes the author.
func (s *Server) authorReclassifyNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(entities.AuthorReclassifyOpID)
	c := t7Common(s.authorReclassifyDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResAuthors, ops.ResOperations)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			p, err := t7DecodeNative[authorReclassifyOpParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			if err := t7LiveOnly(rc, kind); err != nil {
				return struct{}{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			env := authorReclassifyEnv{
				t7Env:     t7NativeEnv(rc),
				Store:     deps.Store,
				Writer:    w,
				StandDown: t7NativeStandDown(rc, deps.StandDownKey),
			}
			msg, err := authorReclassifyCore(rc.Context(), p.AuthorID, env)
			if err != nil {
				env.Log(slog.LevelError, err.Error())
				return struct{}{}, err
			}
			env.Log(slog.LevelInfo, msg)
			return struct{}{}, nil
		},
	})
}

// reclassifyFailure words a failed reclassify for the op's error_message, which
// the Authors view shows. The author is only deleted when nothing was left behind.
func reclassifyFailure(authorID int, out entities.ReclassifyOutcome, err error) error {
	detail := ""
	if len(out.Skipped) > 0 {
		detail = ": " + strings.Join(out.Skipped[:min(len(out.Skipped), 3)], "; ")
	}
	switch {
	case errors.Is(err, entities.ErrAuthorNotFound):
		return fmt.Errorf("author-reclassify: author %d no longer exists", authorID)
	case errors.Is(err, database.ErrAuthorStillLinked):
		return fmt.Errorf("author-reclassify: author %d was kept: %d book(s) moved, but some still credit it%s",
			authorID, out.BooksUpdated, detail)
	case errors.Is(err, entities.ErrReclassifyLeaseLost):
		return fmt.Errorf("author-reclassify: %w after %d book(s); the author was kept", err, out.BooksUpdated)
	}
	return fmt.Errorf("author-reclassify: %w%s", err, detail)
}

// holdReclassifyStandDown stands the library scanner down for the reclassify and
// returns the release to defer: a concurrent library.scan rewrites book_authors on
// the rows the reclassify moves. It fails CLOSED, like the series ops: without an op
// id the undo rows would be unattributable and the gate has no holder key, and a
// scan that will not park refuses the run before any write. The release is always
// safe to call.
//
// This duplicates holdAuthorMergeStandDown from PR #4302 (author merge), which was
// not on main when this was written; swap this for that helper once both land.
func (s *Server) holdReclassifyStandDown(ctx context.Context, opID, reason string) (release func(), err error) {
	if opID == "" {
		return nil, fmt.Errorf(
			"refusing to reclassify without an operation id: its undo-ledger rows would be unattributable and the scan stand-down is keyed by it")
	}
	release, aerr := s.scanGate().AcquireScanStandDown(ctx, opID, reason)
	if aerr != nil {
		return nil, fmt.Errorf("refusing to reclassify while a library.scan is running or queued: %w", aerr)
	}
	if release == nil {
		release = func() {}
	}
	return release, nil
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAuthorReclassifyOp(reg) })
}
