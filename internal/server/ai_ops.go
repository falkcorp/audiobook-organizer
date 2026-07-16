// file: internal/server/ai_ops.go
// version: 1.3.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-07-16

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
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// aiReviewOpParams holds the serializable parameters for the ai.author-review op.
type aiReviewOpParams struct {
	LegacyOpID  string                   `json:"legacy_op_id"`
	Mode        string                   `json:"mode"`
	DedupGroups []dedup.AuthorDedupGroup `json:"dedup_groups,omitempty"`
}

// aiMergeApplySuggestion is the per-item suggestion for the merge-apply op.
// This struct is shared between the HTTP request body and the op params so it
// is JSON-serializable end-to-end.
type aiMergeApplySuggestion struct {
	GroupIndex    int    `json:"group_index"`
	Action        string `json:"action"`
	CanonicalName string `json:"canonical_name"`
	KeepID        int    `json:"keep_id"`
	MergeIDs      []int  `json:"merge_ids"`
	Rename        bool   `json:"rename"`
}

// aiMergeApplyOpParams holds the serializable parameters for the ai.author-merge-apply op.
type aiMergeApplyOpParams struct {
	LegacyOpID  string                   `json:"legacy_op_id"`
	Suggestions []aiMergeApplySuggestion `json:"suggestions"`
}

// RegisterAIAuthorReviewOp registers the "ai.author-review" v2 OperationDef.
func (s *Server) RegisterAIAuthorReviewOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "ai.author-review",
		Plugin:          "ai",
		DisplayName:     "AI Author Duplicate Review",
		Description:     "Uses AI to review and identify duplicate author entries in the library.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "ai.author-review",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkOpenAI},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p aiReviewOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("ai.author-review: decode params: %w", err)
				}
			}
			if p.LegacyOpID == "" {
				return fmt.Errorf("ai.author-review: legacy_op_id is required")
			}
			if p.Mode == "" {
				p.Mode = "groups"
			}

			parser := newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
			store := s.Store()
			progress := registryProgressAdapter{r: reporter}

			switch p.Mode {
			case "groups":
				return handlers.AIReviewGroupsMode(ctx, progress, parser, store, p.LegacyOpID, p.DedupGroups)
			case "full":
				return handlers.AIReviewFullMode(ctx, progress, parser, store, p.LegacyOpID)
			default:
				return fmt.Errorf("ai.author-review: unknown mode: %s", p.Mode)
			}
		},
	})
}

// RegisterAIAuthorMergeApplyOp registers the "ai.author-merge-apply" v2 OperationDef.
func (s *Server) RegisterAIAuthorMergeApplyOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "ai.author-merge-apply",
		Plugin:          "ai",
		DisplayName:     "AI Author Merge Apply",
		Description:     "Applies AI-suggested author merge, rename, alias, and split actions to the library.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         1 * time.Hour,
		ResumePolicy:    opsregistry.ResumeDrop,
		ConcurrencyKey:  "ai.author-merge-apply",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p aiMergeApplyOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("ai.author-merge-apply: decode params: %w", err)
				}
			}
			if p.LegacyOpID == "" {
				return fmt.Errorf("ai.author-merge-apply: legacy_op_id is required")
			}

			store := s.Store()
			progress := registryProgressAdapter{r: reporter}
			suggestions := p.Suggestions
			total := len(suggestions)
			applied := 0
			var applyErrors []string

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
						reassignErrs := reassignBooksFromAuthor(store, mergeID, sug.KeepID)
						applyErrors = append(applyErrors, reassignErrs...)
						// Only delete the merged-away author once EVERY book that
						// credited it has been re-pointed. Deleting while a book still
						// credits mergeID would leave a dangling author reference.
						if len(reassignErrs) > 0 {
							applyErrors = append(applyErrors, fmt.Sprintf("author %d NOT deleted: %d book(s) could not be reassigned", mergeID, len(reassignErrs)))
							continue
						}

						if err := store.DeleteAuthor(mergeID); err != nil {
							applyErrors = append(applyErrors, fmt.Sprintf("delete author %d: %v", mergeID, err))
						} else {
							_ = store.CreateAuthorTombstone(mergeID, sug.KeepID)
						}
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
							reassignErrs := reassignBooksFromAuthor(store, mergeID, sug.KeepID)
							for _, e := range reassignErrs {
								applyErrors = append(applyErrors, e+" (alias)")
							}
							if len(reassignErrs) > 0 {
								applyErrors = append(applyErrors, fmt.Sprintf("aliased author %d NOT deleted: %d book(s) could not be reassigned", mergeID, len(reassignErrs)))
								continue
							}
							if err := store.DeleteAuthor(mergeID); err != nil {
								applyErrors = append(applyErrors, fmt.Sprintf("delete aliased author %d: %v", mergeID, err))
							} else {
								_ = store.CreateAuthorTombstone(mergeID, sug.KeepID)
							}
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

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAIAuthorReviewOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAIAuthorMergeApplyOp(reg) })
}
