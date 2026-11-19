// file: internal/server/ai_ops.go
// version: 1.12.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-11-18

// ai_ops registers the ai.author-review and ai.author-merge-apply
// OperationDefs that previously went through the legacy BridgeQueue.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The params for both ops are handlers.AIReviewOpParams and
// handlers.AIMergeApplyOpParams. This file used to declare its own
// structurally-identical copies so it could decode what the handler enqueued;
// see the comment on those types for why a JSON-tag-only coupling between two
// packages is the wrong way to share a wire shape.

// aiAuthorReviewStore is the reads of the AI author review, both modes: the
// books an author group's members credit, and every author for the full scan.
type aiAuthorReviewStore interface {
	GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error)
	GetAllAuthors() ([]database.Author, error)
}

// aiAuthorReviewEnv is what the shared core takes from its shell (rules R2, R10).
// The review calls the model and stores its suggestions as the run's result; it
// writes no library row, so it has a Store and no Writer.
type aiAuthorReviewEnv struct {
	t7Env
	Store aiAuthorReviewStore
}

// aiAuthorReviewCore is the body of ai.author-review, shared by HEAD's shell and
// the native kind. The suggestions land on this run's own result: it returns them
// and hands them to env.SetResult where the shell has one, as HEAD stored them.
func aiAuthorReviewCore(ctx context.Context, p handlers.AIReviewOpParams, env aiAuthorReviewEnv) (any, error) {
	if p.Mode == "" {
		p.Mode = "groups"
	}

	parser := newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)

	// The suggestions land on this run's own v2 row. They used to be
	// written onto a v1 row the handler minted, which GET
	// /operations/:id/result then read back — that route is now
	// two-keyspace aware, so the id the caller holds resolves either way.
	var result any
	saveResult := func(payload any) error {
		result = payload
		if env.SetResult == nil {
			return nil
		}
		return env.SetResult(payload)
	}

	var err error
	switch p.Mode {
	case "groups":
		err = handlers.AIReviewGroupsMode(ctx, env.reporter(), parser, env.Store, saveResult, p.DedupGroups)
	case "full":
		err = handlers.AIReviewFullMode(ctx, env.reporter(), parser, env.Store, saveResult)
	default:
		err = fmt.Errorf("ai.author-review: unknown mode: %s", p.Mode)
	}
	if err != nil {
		return nil, err
	}
	return result, nil
}

// aiAuthorReviewDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) aiAuthorReviewDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              handlers.AIAuthorReviewDefID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "ai",
		DisplayName:     "AI Author Duplicate Review",
		Description:     "Uses AI to review and identify duplicate author entries in the library.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  handlers.AIAuthorReviewDefID,
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkOpenAI},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p handlers.AIReviewOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("ai.author-review: decode params: %w", err)
				}
			}
			_, err := aiAuthorReviewCore(ctx, p, aiAuthorReviewEnv{t7Env: t7HeadEnv(reporter, true), Store: s.Ops()})
			return err
		},
	}
}

// RegisterAIAuthorReviewOp registers the "ai.author-review" v2 OperationDef.
func (s *Server) RegisterAIAuthorReviewOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.aiAuthorReviewDef())
}

// aiAuthorReviewNative is the native books.ai.author-review kind. The body reads
// the library and the model and writes no store row, so it is declared ReadOnly,
// and it returns the suggestions HEAD stored with ReporterSetResult.
func (s *Server) aiAuthorReviewNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(handlers.AIAuthorReviewDefID)
	c := t7Common(s.aiAuthorReviewDef(), sched)
	c.Effects = tasks.ReadOnly()
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, any]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (any, error) {
			p, err := t7DecodeNative[handlers.AIReviewOpParams](kind, raw)
			if err != nil {
				return nil, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return nil, err
			}
			return aiAuthorReviewCore(rc.Context(), p, aiAuthorReviewEnv{t7Env: t7NativeEnv(rc), Store: deps.Store})
		},
	})
}

// aiMergeApplyStore is the reads of the AI merge apply: the author rows the
// rename and alias cases look up, and whether the store can merge-delete.
type aiMergeApplyStore interface {
	GetAuthorByID(id int) (*database.Author, error)
	// CanMergeDeleteAuthors reports whether the store has the merge-delete
	// capability (database.AuthorMergeDeleter).
	CanMergeDeleteAuthors() bool
}

// aiMergeApplyWriter is the writes of the AI merge apply. The two helpers that
// move books and delete authors are one coarse method each: both journal under
// the op id and assert store capabilities on the store they are handed, so they
// run on the shell's own store rather than through a per-row interface.
type aiMergeApplyWriter interface {
	UpdateAuthorName(id int, name string) error
	CreateAuthorAlias(authorID int, alias, aliasType string) (*database.AuthorAlias, error)
	// ReassignBooksFromAuthor is reassignBooksFromAuthor.
	ReassignBooksFromAuthor(opID string, mergeID, keepID int) (errs, problems []string, booksFailed int)
	// MergeDeleteAuthor is aiMergeDelete.
	MergeDeleteAuthor(opID string, mergeID, keepID int, suffix string) []string
	// InvalidateDedupCache drops the server's duplicate caches.
	InvalidateDedupCache()
}

// aiMergeApplyEnv is what the shared core takes from its shell (rules R2, R10).
type aiMergeApplyEnv struct {
	t7Env
	Store  aiMergeApplyStore
	Writer aiMergeApplyWriter
}

// aiMergeApplyCore is the body of ai.author-merge-apply, shared by HEAD's shell
// and the native kind.
func aiMergeApplyCore(p handlers.AIMergeApplyOpParams, env aiMergeApplyEnv) error {
	store, writer := env.Store, env.Writer
	suggestions := p.Suggestions
	total := len(suggestions)
	applied := 0
	var applyErrors []string
	opID := env.OpID

	// A merge or alias suggestion removes the variant author through the
	// store's merge-delete capability. Without it the variant's aliases
	// could only be deleted with it, so the run is refused before any book
	// is moved; there is no DeleteAuthor fallback.
	for _, sug := range suggestions {
		if sug.Action != "merge" && sug.Action != "alias" {
			continue
		}
		if !store.CanMergeDeleteAuthors() {
			return fmt.Errorf("ai.author-merge-apply: nothing applied: %w", database.ErrAuthorMergeDeleteUnsupported)
		}
		// Every book move and author delete is journaled under the op's id
		// (authormerge), so the apply can be undone. Without one the rows
		// would belong to no operation a revert can name.
		if opID == "" {
			return fmt.Errorf("ai.author-merge-apply: nothing applied: no operation id to journal the undo rows under")
		}
		break
	}

	env.Log(slog.LevelInfo, fmt.Sprintf("Starting AI author review apply: %d suggestion(s)", total))

	for i, sug := range suggestions {
		if env.Canceled != nil && env.Canceled() {
			env.Log(slog.LevelWarn, "Operation cancelled by user")
			return fmt.Errorf("cancelled")
		}

		env.progress(i, total, fmt.Sprintf("Applying suggestion %d/%d...", i+1, total))

		switch sug.Action {
		case "skip":
			env.Log(slog.LevelInfo, fmt.Sprintf("Skipped group %d", sug.GroupIndex))
			continue

		case "rename":
			if sug.KeepID > 0 && sug.CanonicalName != "" {
				if err := writer.UpdateAuthorName(sug.KeepID, dedup.NormalizeAuthorName(sug.CanonicalName)); err != nil {
					applyErrors = append(applyErrors, fmt.Sprintf("rename author %d: %v", sug.KeepID, err))
				} else {
					applied++
					env.Log(slog.LevelInfo, fmt.Sprintf("Renamed author %d to \"%s\"", sug.KeepID, sug.CanonicalName))
				}
			}

		case "merge":
			// Rename canonical if needed
			if sug.Rename && sug.KeepID > 0 && sug.CanonicalName != "" {
				if err := writer.UpdateAuthorName(sug.KeepID, dedup.NormalizeAuthorName(sug.CanonicalName)); err != nil {
					applyErrors = append(applyErrors, fmt.Sprintf("rename before merge %d: %v", sug.KeepID, err))
				}
			}

			// Merge variant authors
			for _, mergeID := range sug.MergeIDs {
				if mergeID == sug.KeepID {
					continue
				}
				reassignErrs, problems, booksFailed := writer.ReassignBooksFromAuthor(opID, mergeID, sug.KeepID)
				applyErrors = append(applyErrors, reassignErrs...)
				applyErrors = append(applyErrors, problems...)
				// Only delete the merged-away author once EVERY book that
				// credited it has been re-pointed. Deleting while a book still
				// credits mergeID would leave a dangling author reference.
				if len(reassignErrs) > 0 {
					applyErrors = append(applyErrors, reassignBlockedMessage("author", mergeID, booksFailed))
					continue
				}

				// reassignBooksFromAuthor moved the junction AND the primary of
				// every book, journaled, so a book left crediting mergeID is
				// one written meanwhile: the delete is gated on that, as every
				// other relink-then-delete path is. aiMergeDelete then
				// journals and merge-deletes the author (aliases onto KeepID,
				// tombstone in the same batch).
				applyErrors = append(applyErrors, writer.MergeDeleteAuthor(opID, mergeID, sug.KeepID, "")...)
			}
			applied++
			env.Log(slog.LevelInfo, fmt.Sprintf("Merged group %d: %d variants into \"%s\"", sug.GroupIndex, len(sug.MergeIDs), sug.CanonicalName))

		case "alias":
			// Keep canonical author, add variants as aliases instead of merging
			if sug.KeepID > 0 && sug.CanonicalName != "" {
				if sug.Rename {
					if err := writer.UpdateAuthorName(sug.KeepID, dedup.NormalizeAuthorName(sug.CanonicalName)); err != nil {
						applyErrors = append(applyErrors, fmt.Sprintf("rename for alias %d: %v", sug.KeepID, err))
					}
				}
				for _, mergeID := range sug.MergeIDs {
					if mergeID == sug.KeepID {
						continue
					}
					variant, err := store.GetAuthorByID(mergeID)
					if err != nil || variant == nil {
						continue
					}
					if _, err := writer.CreateAuthorAlias(sug.KeepID, variant.Name, "pen_name"); err != nil {
						applyErrors = append(applyErrors, fmt.Sprintf("create alias for author %d: %v", sug.KeepID, err))
					}
					// Re-link books and delete the variant author only if every
					// book was successfully re-pointed (see reassignBooksFromAuthor).
					reassignErrs, problems, booksFailed := writer.ReassignBooksFromAuthor(opID, mergeID, sug.KeepID)
					for _, e := range reassignErrs {
						applyErrors = append(applyErrors, e+" (alias)")
					}
					for _, e := range problems {
						applyErrors = append(applyErrors, e+" (alias)")
					}
					if len(reassignErrs) > 0 {
						applyErrors = append(applyErrors, reassignBlockedMessage("aliased author", mergeID, booksFailed))
						continue
					}
					// Gated and journaled as in the merge case above. The
					// alias added to KeepID is not journaled: undoing the
					// apply brings the variant back and leaves that alias.
					applyErrors = append(applyErrors, writer.MergeDeleteAuthor(opID, mergeID, sug.KeepID, " (alias)")...)
				}
				applied++
				env.Log(slog.LevelInfo, fmt.Sprintf("Created aliases for group %d: canonical \"%s\"", sug.GroupIndex, sug.CanonicalName))
			}

		case "split":
			env.Log(slog.LevelInfo, fmt.Sprintf("Split action for group %d — manual intervention needed", sug.GroupIndex))
			applied++
		}
	}

	writer.InvalidateDedupCache()

	resultMsg := fmt.Sprintf("AI review applied: %d actions, %d errors", applied, len(applyErrors))
	env.Log(slog.LevelInfo, resultMsg)
	if len(applyErrors) > 0 {
		errDetail := strings.Join(applyErrors[:min(len(applyErrors), 10)], "; ")
		env.Log(slog.LevelWarn, fmt.Sprintf("Errors: %s", errDetail))
	}

	env.progress(total, total, resultMsg)
	return nil
}

// headAIMergeApply is HEAD's side of both interfaces: the ops store's own
// methods and the two author helpers below, each called as HEAD's body called
// them, plus the server's dedup cache.
type headAIMergeApply struct {
	ServerOpsStore
	s *Server
}

func (h headAIMergeApply) CanMergeDeleteAuthors() bool {
	_, ok := database.AsCapability[database.AuthorMergeDeleter](h.ServerOpsStore)
	return ok
}

func (h headAIMergeApply) ReassignBooksFromAuthor(opID string, mergeID, keepID int) (errs, problems []string, booksFailed int) {
	return reassignBooksFromAuthor(h.ServerOpsStore, opID, mergeID, keepID)
}

func (h headAIMergeApply) MergeDeleteAuthor(opID string, mergeID, keepID int, suffix string) []string {
	return aiMergeDelete(h.ServerOpsStore, opID, mergeID, keepID, suffix)
}

func (h headAIMergeApply) InvalidateDedupCache() { h.s.dedupCache.InvalidateAll() }

// aiMergeApplyDef is HEAD's definition. Its Run is the v2 shell.
func (s *Server) aiMergeApplyDef() opsregistry.OperationDef {
	return opsregistry.OperationDef{
		ID:              handlers.AIAuthorMergeApplyDefID,
		Liveness:        opsregistry.LivenessManual,
		Plugin:          "ai",
		DisplayName:     "AI Author Merge Apply",
		Description:     "Applies AI-suggested author merge, rename, alias, and split actions to the library.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  handlers.AIAuthorMergeApplyDefID,
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p handlers.AIMergeApplyOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("ai.author-merge-apply: decode params: %w", err)
				}
			}
			h := headAIMergeApply{ServerOpsStore: s.Ops(), s: s}
			return aiMergeApplyCore(p, aiMergeApplyEnv{t7Env: t7HeadEnv(reporter, true), Store: h, Writer: h})
		},
	}
}

// RegisterAIAuthorMergeApplyOp registers the "ai.author-merge-apply" v2 OperationDef.
func (s *Server) RegisterAIAuthorMergeApplyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(s.aiMergeApplyDef())
}

// aiMergeApplyNative is the native books.ai.author-merge-apply kind. It renames,
// aliases and merges authors and re-points their books and credits, so it
// writes books and authors and journals under the run id.
func (s *Server) aiMergeApplyNative(deps t7Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	kind := t7KindFor(handlers.AIAuthorMergeApplyDefID)
	c := t7Common(s.aiMergeApplyDef(), sched)
	c.Effects = t7Writes(ops.ResBooks, ops.ResAuthors, ops.ResOperations)
	return tasks.Task(kind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			p, err := t7DecodeNative[handlers.AIMergeApplyOpParams](kind, raw)
			if err != nil {
				return struct{}{}, err
			}
			if err := t7NeedStore(kind, deps); err != nil {
				return struct{}{}, err
			}
			w, err := t7NewWriter(kind, deps, rc)
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, aiMergeApplyCore(p, aiMergeApplyEnv{t7Env: t7NativeEnv(rc), Store: deps.Store, Writer: w})
		},
	})
}

// aiMergeDelete journals and merge-deletes mergeID into keepID once no book
// credits it (database.VerifyAuthorUnlinked), through authormerge.MergeDelete:
// the author_merge_delete row the revert engine reverses, written before the
// delete. It returns the errors to report, each suffixed with suffix.
func aiMergeDelete(store ServerOpsStore, opID string, mergeID, keepID int, suffix string) []string {
	if err := database.VerifyAuthorUnlinked(store, mergeID); err != nil {
		return []string{fmt.Sprintf("author %d NOT deleted: %v%s", mergeID, err, suffix)}
	}
	from, err := store.GetAuthorByID(mergeID)
	if err != nil {
		return []string{fmt.Sprintf("author %d NOT deleted: read it: %v%s", mergeID, err, suffix)}
	}
	// GetAuthorByID follows a tombstone: an id already merged away reads as its
	// survivor, and there is nothing left to delete.
	if from == nil || from.ID != mergeID {
		return nil
	}
	keep, err := store.GetAuthorByID(keepID)
	if err != nil || keep == nil || keep.ID != keepID {
		return []string{fmt.Sprintf("author %d NOT deleted: the kept author %d could not be read (%v)%s", mergeID, keepID, err, suffix)}
	}
	del, err := authormerge.MergeDelete(store, opID, *from, *keep)
	if err != nil {
		return []string{fmt.Sprintf("author %d NOT deleted: %v%s", mergeID, err, suffix)}
	}
	var out []string
	for _, p := range del.Problems {
		out = append(out, fmt.Sprintf("author %d: %s%s", mergeID, p, suffix))
	}
	return out
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAIAuthorReviewOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAIAuthorMergeApplyOp(reg) })
}
