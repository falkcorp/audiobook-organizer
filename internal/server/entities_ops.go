// file: internal/server/entities_ops.go
// version: 1.1.0
// guid: 3f7e2a91-b4c6-4d85-9e13-7a2f10c84d32

// entities_ops registers the UOS-02 OperationDefs for author entity
// operations: author-merge and resolve-production-author. Each def is
// registered via addOpRegistrar in init(), so no edits to server.go are
// required.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	ulid "github.com/oklog/ulid/v2"
)

// authorMergeOpParams holds the parameters for the entities.author-merge op.
type authorMergeOpParams struct {
	LegacyOpID string `json:"legacy_op_id"`
	KeepID     int    `json:"keep_id"`
	MergeIDs   []int  `json:"merge_ids"`
	KeepName   string `json:"keep_name"`
}

// resolveProductionAuthorOpParams holds the parameters for the
// entities.resolve-production-author op.
type resolveProductionAuthorOpParams struct {
	LegacyOpID     string `json:"legacy_op_id"`
	AuthorID       int    `json:"author_id"`
	ProdAuthorName string `json:"prod_author_name"`
}

// RegisterAuthorMergeOp registers the "entities.author-merge" v2 OperationDef.
func (s *Server) RegisterAuthorMergeOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "entities.author-merge",
		Plugin:          "entities",
		DisplayName:     "Author Merge",
		Description:     "Merge one or more author records into a single canonical author, relinking all associated books.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeRestart,
		ConcurrencyKey:  "entities.author-merge",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p authorMergeOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("author-merge: decode params: %w", err)
				}
			}

			store := s.Store()
			opID := p.LegacyOpID
			keepID := p.KeepID
			mergeIDs := p.MergeIDs
			keepName := p.KeepName

			progress := registryProgressAdapter{r: reporter}

			_ = progress.Log("info", fmt.Sprintf("Merging %d author(s) into %q", len(mergeIDs), keepName), nil)
			_ = progress.UpdateProgress(0, len(mergeIDs), "Starting author merge...")

			merged := 0
			var mergeErrors []string
			for i, mergeID := range mergeIDs {
				if progress.IsCanceled() {
					return fmt.Errorf("cancelled")
				}
				if mergeID == keepID {
					continue
				}
				books, err := store.GetBooksByAuthorIDWithRoleCore(mergeID)
				if err != nil {
					mergeErrors = append(mergeErrors, fmt.Sprintf("failed to get books for author %d: %v", mergeID, err))
					continue
				}

				mergeAuthor, _ := store.GetAuthorByID(mergeID)
				mergeAuthorName := ""
				if mergeAuthor != nil {
					mergeAuthorName = mergeAuthor.Name
				}

				for _, book := range books {
					bookAuthors, err := store.GetBookAuthors(book.ID)
					if err != nil {
						continue
					}
					hasKeep := false
					for _, ba := range bookAuthors {
						if ba.AuthorID == keepID {
							hasKeep = true
							break
						}
					}
					var newAuthors []database.BookAuthor
					for _, ba := range bookAuthors {
						if ba.AuthorID == mergeID {
							if !hasKeep {
								ba.AuthorID = keepID
								newAuthors = append(newAuthors, ba)
								hasKeep = true
							}
						} else {
							newAuthors = append(newAuthors, ba)
						}
					}
					if err := store.SetBookAuthors(book.ID, newAuthors); err != nil {
						mergeErrors = append(mergeErrors, fmt.Sprintf("failed to update book %s: %v", book.ID, err))
					} else {
						_ = store.CreateOperationChange(&database.OperationChange{
							ID:          ulid.Make().String(),
							OperationID: opID,
							BookID:      book.ID,
							ChangeType:  "author_reassign",
							FieldName:   "book_authors",
							OldValue:    fmt.Sprintf("author_id:%d (%s)", mergeID, mergeAuthorName),
							NewValue:    fmt.Sprintf("author_id:%d (%s)", keepID, keepName),
						})
					}

					// Sync the denormalized `book.AuthorID` pointer on the Book row itself.
					// SetBookAuthors above updates the join table, but callers that read the Book
					// struct directly expect book.AuthorID to match the primary author in the join
					// table. Without this sync, the field still points at the losing author ID.
					//
					// Backlog 7.11 — found while investigating the merge ITL cleanup bug (#251).
					current, gbErr := store.GetBookByID(book.ID)
					if gbErr != nil || current == nil {
						continue
					}
					if current.AuthorID != nil && *current.AuthorID == mergeID {
						newID := keepID
						current.AuthorID = &newID
						if _, upErr := store.UpdateBook(book.ID, current); upErr != nil {
							slog.Warn("author merge failed to sync denormalized AuthorID on book", "book", book.ID, "upErr", upErr)
						}
					}
				}

				if err := store.DeleteAuthor(mergeID); err != nil {
					mergeErrors = append(mergeErrors, fmt.Sprintf("failed to delete author %d: %v", mergeID, err))
				} else {
					_ = store.CreateAuthorTombstone(mergeID, keepID)
					_ = store.CreateOperationChange(&database.OperationChange{
						ID:          ulid.Make().String(),
						OperationID: opID,
						BookID:      "",
						ChangeType:  "author_delete",
						FieldName:   "author",
						OldValue:    fmt.Sprintf("%d:%s", mergeID, mergeAuthorName),
						NewValue:    fmt.Sprintf("merged_into:%d:%s", keepID, keepName),
					})
					merged++
				}

				_ = progress.UpdateProgress(i+1, len(mergeIDs),
					fmt.Sprintf("Merged %d/%d authors", i+1, len(mergeIDs)))
			}

			resultMsg := fmt.Sprintf("Author merge complete: merged %d, %d errors", merged, len(mergeErrors))
			_ = progress.Log("info", resultMsg, nil)
			if len(mergeErrors) > 0 {
				errDetail := strings.Join(mergeErrors[:min(len(mergeErrors), 10)], "; ")
				_ = progress.Log("warn", fmt.Sprintf("Errors: %s", errDetail), nil)
			}
			s.dedupCache.InvalidateAll()
			s.authorsCache.InvalidateAll()
			return nil
		},
	})
}

// RegisterResolveProductionAuthorOp registers the
// "entities.resolve-production-author" v2 OperationDef.
func (s *Server) RegisterResolveProductionAuthorOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:              "entities.resolve-production-author",
		Plugin:          "entities",
		DisplayName:     "Resolve Production Author",
		Description:     "Attempt to discover real authors for books attributed to a production company via metadata lookups and AI cover analysis.",
		DefaultPriority: opsregistry.PriorityNormal,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ResumePolicy:    opsregistry.ResumeRestart,
		ConcurrencyKey:  "entities.resolve-production-author",
		Permissions:     []auth.Permission{auth.PermLibraryEditMetadata},
		Capabilities:    []opsregistry.Capability{opsregistry.CapLibraryRead, opsregistry.CapLibraryWrite, opsregistry.CapNetworkGeneric},
		Run: func(ctx context.Context, rawParams json.RawMessage, reporter opsregistry.Reporter) error {
			var p resolveProductionAuthorOpParams
			if len(rawParams) > 0 {
				if err := json.Unmarshal(rawParams, &p); err != nil {
					return fmt.Errorf("resolve-production-author: decode params: %w", err)
				}
			}

			store := s.Store()
			authorID := p.AuthorID
			prodAuthorName := p.ProdAuthorName

			progress := registryProgressAdapter{r: reporter}

			books, err := store.GetBooksByAuthorIDWithRoleCore(authorID)
			if err != nil {
				return fmt.Errorf("failed to get books: %w", err)
			}
			_ = progress.Log("info", fmt.Sprintf("Resolving %d books for production company %q", len(books), prodAuthorName), nil)

			resolved := 0
			failed := 0
			for i, book := range books {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				_ = progress.UpdateProgress(i, len(books), fmt.Sprintf("Processing %d/%d: %s", i+1, len(books), book.Title))

				// Try metadata fetch by title only
				resp, fetchErr := s.metadataFetchService.FetchMetadataForBookByTitle(book.ID)
				if fetchErr == nil && resp != nil && resp.Book != nil && resp.Book.AuthorID != nil {
					// Check if the found author is different from the production company
					newAuthor, _ := store.GetAuthorByID(*resp.Book.AuthorID)
					if newAuthor != nil && !dedup.IsProductionCompany(newAuthor.Name) {
						_ = progress.Log("info", fmt.Sprintf("Resolved %q → author %q (source: %s)", book.Title, newAuthor.Name, resp.Source), nil)
						// Reclassify production company as publisher
						if book.Publisher == nil || *book.Publisher == "" {
							pub := prodAuthorName
							book.Publisher = &pub
							store.UpdateBook(book.ID, &database.Book{Publisher: &pub})
						}
						resolved++
						continue
					}
				}

				// If metadata failed and AI is enabled, try cover art analysis
				aiParser := newAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
				if aiParser.IsEnabled() && book.FilePath != "" {
					imgData, mime, imgErr := metadata.ExtractCoverArtBytes(book.FilePath)
					if imgErr == nil && len(imgData) > 0 {
						parsed, aiErr := aiParser.ParseCoverArt(ctx, imgData, mime)
						if aiErr == nil && parsed != nil && parsed.Author != "" && parsed.Confidence != "low" {
							_ = progress.Log("info", fmt.Sprintf("AI cover analysis for %q found author: %q (confidence: %s)", book.Title, parsed.Author, parsed.Confidence), nil)
							// Look up or create the discovered author
							existing, _ := store.GetAuthorByName(parsed.Author)
							if existing == nil {
								existing, _ = store.CreateAuthor(parsed.Author)
							}
							if existing != nil {
								aid := existing.ID
								book.AuthorID = &aid
								store.UpdateBook(book.ID, &database.Book{AuthorID: &aid})
								// Update book_authors
								bookAuthors, _ := store.GetBookAuthors(book.ID)
								var updated []database.BookAuthor
								for _, ba := range bookAuthors {
									if ba.AuthorID != authorID {
										updated = append(updated, ba)
									}
								}
								updated = append(updated, database.BookAuthor{
									BookID:   book.ID,
									AuthorID: existing.ID,
									Role:     "author",
									Position: 0,
								})
								store.SetBookAuthors(book.ID, updated)
								resolved++
								continue
							}
						}
					}
				}

				failed++
				_ = progress.Log("debug", fmt.Sprintf("Could not resolve author for %q", book.Title), nil)
			}

			if s.dedupCache != nil {
				s.dedupCache.Invalidate("author-duplicates")
			}
			s.authorsCache.InvalidateAll()

			resultMsg := fmt.Sprintf("Resolved %d/%d books for %q (%d unresolved)", resolved, len(books), prodAuthorName, failed)
			_ = progress.Log("info", resultMsg, nil)
			_ = progress.UpdateProgress(len(books), len(books), resultMsg)
			return nil
		},
	})
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAuthorMergeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterResolveProductionAuthorOp(reg) })
}
