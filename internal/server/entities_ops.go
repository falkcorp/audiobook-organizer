// file: internal/server/entities_ops.go
// version: 1.2.0
// guid: 3f7e2a91-b4c6-4d85-9e13-7a2f10c84d32
// last-edited: 2026-07-13

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
			hydrateSkipped := 0
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
						// Reclassify production company as publisher. Hydrate the
						// full stored row before the write (see
						// assignPublisherPreservingRecord) so the full-replace
						// UpdateBook cannot wipe Title/FilePath/ratings/etc.
						if book.Publisher == nil || *book.Publisher == "" {
							if err := assignPublisherPreservingRecord(store, book.ID, prodAuthorName); err != nil {
								_ = progress.Log("warn", fmt.Sprintf("Skipped publisher reclassify for %q (hydrate failed, record left intact): %v", book.Title, err), nil)
								hydrateSkipped++
							}
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
								// Assign the discovered author. Hydrate the full
								// stored row before the AuthorID write (see
								// assignResolvedAuthorPreservingRecord) so the
								// full-replace UpdateBook cannot wipe the record.
								// Fail-closed: on hydrate error nothing is written
								// (neither the Book row nor the join), leaving the
								// book consistently attributed to the production
								// company rather than half-resolved or wiped.
								if err := assignResolvedAuthorPreservingRecord(store, book.ID, existing.ID, authorID); err != nil {
									_ = progress.Log("warn", fmt.Sprintf("Skipped author resolve for %q (hydrate failed, record left intact): %v", book.Title, err), nil)
									hydrateSkipped++
									failed++
									continue
								}
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

			resultMsg := fmt.Sprintf("Resolved %d/%d books for %q (%d unresolved, %d skipped on hydrate error)", resolved, len(books), prodAuthorName, failed, hydrateSkipped)
			_ = progress.Log("info", resultMsg, nil)
			_ = progress.UpdateProgress(len(books), len(books), resultMsg)
			return nil
		},
	})
}

// assignPublisherPreservingRecord reclassifies a production company as the
// book's Publisher WITHOUT wiping the rest of the stored record.
//
// database.Store.UpdateBook is a FULL-REPLACE write: it marshals the whole Book
// passed to it (only seven heavy Description/BookSig* fields are restored from
// the old row). Passing a near-empty literal such as &database.Book{Publisher:…}
// therefore wipes Title, FilePath (which also corrupts the book:path: index),
// AuthorID, ratings, media-info, and everything else. This helper instead
// hydrates the current full row via GetBookByID and mutates only Publisher, so
// every other field survives the write.
//
// Fail-closed: if hydration fails it returns the error and writes NOTHING. A
// skipped publisher tag is far better than a wiped record; the caller counts the
// skip and moves on to the next book.
func assignPublisherPreservingRecord(store database.Store, bookID, publisher string) error {
	full, err := store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("hydrate book %s before publisher write: %w", bookID, err)
	}
	if full == nil {
		return fmt.Errorf("hydrate book %s before publisher write: not found", bookID)
	}
	pub := publisher
	full.Publisher = &pub
	if _, err := store.UpdateBook(bookID, full); err != nil {
		return fmt.Errorf("update book %s publisher: %w", bookID, err)
	}
	return nil
}

// assignResolvedAuthorPreservingRecord points a book at a newly discovered
// author: it sets the denormalized Book.AuthorID and rewrites the book_authors
// join to drop the production-company author and add the resolved one — WITHOUT
// wiping the rest of the stored record (see assignPublisherPreservingRecord for
// why a bare UpdateBook literal is catastrophic).
//
// Fail-closed: if hydration fails it returns the error before touching anything,
// so neither the Book row nor the join is written and the book stays consistently
// attributed to the production company (unresolved) rather than half-applied or
// wiped. The join rewrite itself is best-effort: an error there is logged by the
// caller path but does not roll back the AuthorID write (matching prior behavior).
func assignResolvedAuthorPreservingRecord(store database.Store, bookID string, resolvedAuthorID, prodAuthorID int) error {
	full, err := store.GetBookByID(bookID)
	if err != nil {
		return fmt.Errorf("hydrate book %s before author write: %w", bookID, err)
	}
	if full == nil {
		return fmt.Errorf("hydrate book %s before author write: not found", bookID)
	}
	aid := resolvedAuthorID
	full.AuthorID = &aid
	if _, err := store.UpdateBook(bookID, full); err != nil {
		return fmt.Errorf("update book %s author: %w", bookID, err)
	}

	// Rewrite the book_authors join: drop the production-company author, add the
	// resolved author. Best-effort, as before — a failure here is logged but does
	// not fail the resolution (the denormalized AuthorID already points correctly).
	bookAuthors, _ := store.GetBookAuthors(bookID)
	var updated []database.BookAuthor
	for _, ba := range bookAuthors {
		if ba.AuthorID != prodAuthorID {
			updated = append(updated, ba)
		}
	}
	updated = append(updated, database.BookAuthor{
		BookID:   bookID,
		AuthorID: resolvedAuthorID,
		Role:     "author",
		Position: 0,
	})
	if err := store.SetBookAuthors(bookID, updated); err != nil {
		slog.Warn("resolve-production-author: failed to rewrite book_authors join", "book", bookID, "err", err)
	}
	return nil
}

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterAuthorMergeOp(reg) })
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error { return s.RegisterResolveProductionAuthorOp(reg) })
}
