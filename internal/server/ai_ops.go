// file: internal/server/ai_ops.go
// version: 1.10.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-10-14

// ai_ops registers the ai.author-review and ai.author-merge-apply
// OperationDefs that previously went through the legacy BridgeQueue.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/authormerge"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// The params for both ops are handlers.AIReviewOpParams and
// handlers.AIMergeApplyOpParams. This file used to declare its own
// structurally-identical copies so it could decode what the handler enqueued;
// see the comment on those types for why a JSON-tag-only coupling between two
// packages is the wrong way to share a wire shape.

// RegisterAIAuthorReviewOp registers the "ai.author-review" v2 OperationDef.
func (s *Server) RegisterAIAuthorReviewOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
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
			if p.Mode == "" {
				p.Mode = "groups"
			}

			parser := newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
			store := s.Ops()
			progress := registryProgressAdapter{r: reporter}

			// The suggestions land on this run's own v2 row. They used to be
			// written onto a v1 row the handler minted, which GET
			// /operations/:id/result then read back — that route is now
			// two-keyspace aware, so the id the caller holds resolves either way.
			saveResult := func(payload any) error {
				return opsregistry.ReporterSetResult(reporter, payload)
			}

			switch p.Mode {
			case "groups":
				return handlers.AIReviewGroupsMode(ctx, progress, parser, store, saveResult, p.DedupGroups)
			case "full":
				return handlers.AIReviewFullMode(ctx, progress, parser, store, saveResult)
			default:
				return fmt.Errorf("ai.author-review: unknown mode: %s", p.Mode)
			}
		},
	})
}

// RegisterAIAuthorMergeApplyOp registers the "ai.author-merge-apply" v2 OperationDef.
func (s *Server) RegisterAIAuthorMergeApplyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
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
			store := s.Ops()
			progress := registryProgressAdapter{r: reporter}
			suggestions := p.Suggestions
			total := len(suggestions)
			applied := 0
			var applyErrors []string
			opID := opsregistry.ReporterOpID(reporter)

			// A merge or alias suggestion removes the variant author through the
			// store's merge-delete capability. Without it the variant's aliases
			// could only be deleted with it, so the run is refused before any book
			// is moved; there is no DeleteAuthor fallback.
			for _, sug := range suggestions {
				if sug.Action != "merge" && sug.Action != "alias" {
					continue
				}
				if _, ok := database.AsCapability[database.AuthorMergeDeleter](store); !ok {
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

			_ = progress.Log("info", fmt.Sprintf("Starting AI author review apply: %d suggestion(s)", total), nil)

			for i, sug := range suggestions {
				if progress.IsCanceled() {
					_ = progress.Log("warn", "Operation cancelled by user", nil)
					return fmt.Errorf("cancelled")
				}

				_ = progress.UpdateProgress(i, total, fmt.Sprintf("Applying suggestion %d/%d...", i+1, total))

				switch sug.Action {
				case "skip":
					_ = progress.Log("info", fmt.Sprintf("Skipped group %d", sug.GroupIndex), nil)
					continue

				case "rename":
					if sug.KeepID > 0 && sug.CanonicalName != "" {
						if err := store.UpdateAuthorName(sug.KeepID, dedup.NormalizeAuthorName(sug.CanonicalName)); err != nil {
							applyErrors = append(applyErrors, fmt.Sprintf("rename author %d: %v", sug.KeepID, err))
						} else {
							applied++
							_ = progress.Log("info", fmt.Sprintf("Renamed author %d to \"%s\"", sug.KeepID, sug.CanonicalName), nil)
						}
					}

				case "merge":
					// Rename canonical if needed
					if sug.Rename && sug.KeepID > 0 && sug.CanonicalName != "" {
						if err := store.UpdateAuthorName(sug.KeepID, dedup.NormalizeAuthorName(sug.CanonicalName)); err != nil {
							applyErrors = append(applyErrors, fmt.Sprintf("rename before merge %d: %v", sug.KeepID, err))
						}
					}

					// Merge variant authors
					for _, mergeID := range sug.MergeIDs {
						if mergeID == sug.KeepID {
							continue
						}
						reassignErrs, problems := reassignBooksFromAuthor(store, opID, mergeID, sug.KeepID)
						applyErrors = append(applyErrors, reassignErrs...)
						applyErrors = append(applyErrors, problems...)
						// Only delete the merged-away author once EVERY book that
						// credited it has been re-pointed. Deleting while a book still
						// credits mergeID would leave a dangling author reference.
						if len(reassignErrs) > 0 {
							applyErrors = append(applyErrors, fmt.Sprintf("author %d NOT deleted: %d book(s) could not be reassigned", mergeID, len(reassignErrs)))
							continue
						}

						// reassignBooksFromAuthor moved the junction AND the primary of
						// every book, journaled, so a book left crediting mergeID is
						// one written meanwhile: the delete is gated on that, as every
						// other relink-then-delete path is. aiMergeDelete then
						// journals and merge-deletes the author (aliases onto KeepID,
						// tombstone in the same batch).
						applyErrors = append(applyErrors, aiMergeDelete(store, opID, mergeID, sug.KeepID, "")...)
					}
					applied++
					_ = progress.Log("info", fmt.Sprintf("Merged group %d: %d variants into \"%s\"", sug.GroupIndex, len(sug.MergeIDs), sug.CanonicalName), nil)

				case "alias":
					// Keep canonical author, add variants as aliases instead of merging
					if sug.KeepID > 0 && sug.CanonicalName != "" {
						if sug.Rename {
							if err := store.UpdateAuthorName(sug.KeepID, dedup.NormalizeAuthorName(sug.CanonicalName)); err != nil {
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
							if _, err := store.CreateAuthorAlias(sug.KeepID, variant.Name, "pen_name"); err != nil {
								applyErrors = append(applyErrors, fmt.Sprintf("create alias for author %d: %v", sug.KeepID, err))
							}
							// Re-link books and delete the variant author only if every
							// book was successfully re-pointed (see reassignBooksFromAuthor).
							reassignErrs, problems := reassignBooksFromAuthor(store, opID, mergeID, sug.KeepID)
							for _, e := range reassignErrs {
								applyErrors = append(applyErrors, e+" (alias)")
							}
							for _, e := range problems {
								applyErrors = append(applyErrors, e+" (alias)")
							}
							if len(reassignErrs) > 0 {
								applyErrors = append(applyErrors, fmt.Sprintf("aliased author %d NOT deleted: %d book(s) could not be reassigned", mergeID, len(reassignErrs)))
								continue
							}
							// Gated and journaled as in the merge case above. The
							// alias added to KeepID is not journaled: undoing the
							// apply brings the variant back and leaves that alias.
							applyErrors = append(applyErrors, aiMergeDelete(store, opID, mergeID, sug.KeepID, " (alias)")...)
						}
						applied++
						_ = progress.Log("info", fmt.Sprintf("Created aliases for group %d: canonical \"%s\"", sug.GroupIndex, sug.CanonicalName), nil)
					}

				case "split":
					_ = progress.Log("info", fmt.Sprintf("Split action for group %d — manual intervention needed", sug.GroupIndex), nil)
					applied++
				}
			}

			s.dedupCache.InvalidateAll()

			resultMsg := fmt.Sprintf("AI review applied: %d actions, %d errors", applied, len(applyErrors))
			_ = progress.Log("info", resultMsg, nil)
			if len(applyErrors) > 0 {
				errDetail := strings.Join(applyErrors[:min(len(applyErrors), 10)], "; ")
				_ = progress.Log("warn", fmt.Sprintf("Errors: %s", errDetail), nil)
			}

			_ = progress.UpdateProgress(total, total, resultMsg)
			return nil
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
