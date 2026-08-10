// file: internal/database/memdb_schema.go
// version: 1.1.0
// guid: a1b2c3d4-mema-aaaa-aaaa-000000000002

package database

import "github.com/hashicorp/go-memdb"

// Table names for the in-memory query store.
const (
	memTableBooks            = "books"
	memTableAuthors          = "authors"
	memTableSeries           = "series"
	memTableBookFiles        = "book_files"
	memTableNarrators        = "narrators"
	memTableBookAuthors      = "book_authors"
	memTableBookNarrators    = "book_narrators"
	memTableImportPaths      = "import_paths"
	memTableAuthorAliases    = "author_aliases"
	memTableBlockedHashes    = "blocked_hashes"
)

// Index names.
const (
	memIdxID                = "id"
	memIdxName              = "name"
	memIdxAuthorID          = "author_id"
	memIdxSeriesID          = "series_id"
	memIdxBookID            = "book_id"
	memIdxNarratorID        = "narrator_id"
	memIdxFilePath          = "file_path"
	memIdxFileHash          = "file_hash"
	memIdxMissing           = "missing"
	memIdxIsPrimaryVersion  = "is_primary_version"
	memIdxMarkedForDeletion = "marked_for_deletion"
	memIdxVersionGroupID    = "version_group_id"
	memIdxITunesPID         = "itunes_persistent_id"
	memIdxTitle             = "title"
	memIdxPath              = "path"
	memIdxEnabled           = "enabled"
	memIdxAliasName         = "alias_name"
	memIdxHash              = "hash"
	memIdxDelugeHash        = "deluge_hash"

	// Sorted secondary indexes for the library list. Each turns a
	// materialise-the-whole-filtered-set-and-sort into an ordered streaming
	// walk, the way memIdxTitle already does for title. See
	// memdb_sort_indexers.go for the key encoding and the field selection.
	//
	// Six physical indexes serve nine sort keys: duration/bitrate/file_size
	// each have an alias spelling that maps to the same index.
	memIdxSortAuthor    = "sort_author"
	memIdxSortNarrator  = "sort_narrator"
	memIdxSortSeries    = "sort_series"
	memIdxSortYear      = "sort_year"
	memIdxSortCreatedAt = "sort_created_at"
	memIdxSortUpdatedAt = "sort_updated_at"
	memIdxSortDuration  = "sort_duration"
	memIdxSortBitrate   = "sort_bitrate"
	memIdxSortFileSize  = "sort_file_size"
)

// memdbSchema returns the complete schema for the in-memory query layer.
// PebbleDB remains source of truth; this schema is derived/rebuilt from Pebble
// on startup and kept in sync via write-through.
func memdbSchema() *memdb.DBSchema {
	s := baseMemdbSchema()
	// Sorted secondary indexes are attached conditionally: each costs real
	// memory (~146 MB per key at prod scale — see config.EnabledSortIndexes),
	// so they are opt-in rather than always-on. With none enabled this is a
	// no-op and the schema is byte-for-byte what it was before they existed.
	attachEnabledSortIndexes(s)
	return s
}

// attachEnabledSortIndexes registers a sorted index on the books table for
// each enabled sort field. Alias spellings collapse to one index.
//
// Indexers are attached here rather than in the schema literal so that the
// enabled set is consulted exactly once, at schema-build time, and the
// schema and CanPushDownSort can never disagree about which indexes exist.
func attachEnabledSortIndexes(s *memdb.DBSchema) {
	books := s.Tables[memTableBooks]
	if books == nil {
		return
	}

	// AllowMissing is deliberately NOT set on any of these: each indexer
	// emits an explicit "missing" key for books without a value, so every
	// book appears in every index. Letting rows be absent instead would
	// silently drop them from the library page whenever that sort is
	// selected — the failure titleSortIndex's comment documents.
	all := map[string]*memdb.IndexSchema{
		memIdxSortAuthor: {
			Name:    memIdxSortAuthor,
			Indexer: bookStringSortIndex{name: memIdxSortAuthor, get: bookAuthorSortValue},
		},
		memIdxSortNarrator: {
			Name:    memIdxSortNarrator,
			Indexer: bookStringSortIndex{name: memIdxSortNarrator, get: bookNarratorSortValue},
		},
		memIdxSortSeries: {
			Name:    memIdxSortSeries,
			Indexer: bookStringSortIndex{name: memIdxSortSeries, get: bookSeriesSortValue},
		},
		memIdxSortYear: {
			Name:    memIdxSortYear,
			Indexer: bookIntSortIndex{name: memIdxSortYear, get: bookYearSortValue},
		},
		memIdxSortCreatedAt: {
			Name:    memIdxSortCreatedAt,
			Indexer: bookIntSortIndex{name: memIdxSortCreatedAt, get: bookCreatedAtSortValue},
		},
		memIdxSortUpdatedAt: {
			Name:    memIdxSortUpdatedAt,
			Indexer: bookIntSortIndex{name: memIdxSortUpdatedAt, get: bookUpdatedAtSortValue},
		},
		memIdxSortDuration: {
			Name:    memIdxSortDuration,
			Indexer: bookIntSortIndex{name: memIdxSortDuration, get: bookDurationSortValue},
		},
		memIdxSortBitrate: {
			Name:    memIdxSortBitrate,
			Indexer: bookIntSortIndex{name: memIdxSortBitrate, get: bookBitrateSortValue},
		},
		memIdxSortFileSize: {
			Name:    memIdxSortFileSize,
			Indexer: bookIntSortIndex{name: memIdxSortFileSize, get: bookFileSizeSortValue},
		},
	}

	for name := range enabledSortIndexNames() {
		if idx, ok := all[name]; ok {
			books.Indexes[name] = idx
		}
	}
}

func baseMemdbSchema() *memdb.DBSchema {
	return &memdb.DBSchema{
		Tables: map[string]*memdb.TableSchema{
			memTableBooks: {
				Name: memTableBooks,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.StringFieldIndex{Field: "ID"},
					},
					memIdxAuthorID: {
						Name:         memIdxAuthorID,
						AllowMissing: true,
						Indexer:      &nullableIntFieldIndex{Field: "AuthorID"},
					},
					memIdxSeriesID: {
						Name:         memIdxSeriesID,
						AllowMissing: true,
						Indexer:      &nullableIntFieldIndex{Field: "SeriesID"},
					},
					memIdxIsPrimaryVersion: {
						// Default nil → true to match SQL semantics (column default true)
						Name: memIdxIsPrimaryVersion,
						Indexer: &effectiveBoolFieldIndex{
							Field:   "IsPrimaryVersion",
							Default: true,
						},
					},
					memIdxMarkedForDeletion: {
						// Default nil → false (column default false)
						Name: memIdxMarkedForDeletion,
						Indexer: &effectiveBoolFieldIndex{
							Field:   "MarkedForDeletion",
							Default: false,
						},
					},
					memIdxVersionGroupID: {
						Name:         memIdxVersionGroupID,
						AllowMissing: true,
						Indexer:      &nullableStringFieldIndex{Field: "VersionGroupID"},
					},
					memIdxITunesPID: {
						// nullableStringFieldIndex skips rows where the *string
						// field is nil OR empty, so the walker over this index
						// only sees books that actually have an iTunes
						// persistent ID — exactly the filter the iTunes
						// handlers want.
						Name:         memIdxITunesPID,
						AllowMissing: true,
						Indexer:      &nullableStringFieldIndex{Field: "ITunesPersistentID"},
					},
					memIdxFilePath: {
						// NOT unique. Pebble doesn't enforce file_path uniqueness;
						// real data has duplicates (soft-deleted versions, dedup
						// candidates). Declaring Unique here caused warmup to
						// abort on the first conflict and silently leave memdb
						// empty — which made the library list look empty.
						Name:         memIdxFilePath,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "FilePath"},
					},
					memIdxTitle: {
						// Custom indexer so every book gets a sort key,
						// even when Title is empty (scanned-but-not-enriched,
						// quarantined, etc.). Falls back to OriginalFilename,
						// then a "~" sentinel that sorts to the end. Without
						// this, sort_by=title would silently drop titleless
						// books from the library page.
						Name:    memIdxTitle,
						Indexer: titleSortIndex{},
					},
				},
			},

			memTableAuthors: {
				Name: memTableAuthors,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.IntFieldIndex{Field: "ID"},
					},
					memIdxName: {
						// AllowMissing: legacy/placeholder rows may have empty Name.
						Name:         memIdxName,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "Name", Lowercase: true},
					},
				},
			},

			memTableSeries: {
				Name: memTableSeries,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.IntFieldIndex{Field: "ID"},
					},
					memIdxName: {
						// AllowMissing: legacy/placeholder rows may have empty Name.
						Name:         memIdxName,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "Name", Lowercase: true},
					},
					memIdxAuthorID: {
						Name:         memIdxAuthorID,
						AllowMissing: true,
						Indexer:      &nullableIntFieldIndex{Field: "AuthorID"},
					},
				},
			},

			memTableBookFiles: {
				Name: memTableBookFiles,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.StringFieldIndex{Field: "ID"},
					},
					memIdxBookID: {
						Name:    memIdxBookID,
						Indexer: &memdb.StringFieldIndex{Field: "BookID"},
					},
					memIdxFileHash: {
						Name:         memIdxFileHash,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "FileHash"},
					},
					memIdxMissing: {
						Name:    memIdxMissing,
						Indexer: &plainBoolFieldIndex{Field: "Missing"},
					},
					memIdxFilePath: {
						Name:         memIdxFilePath,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "FilePath"},
					},
					memIdxDelugeHash: {
						// Sparse: only rows with non-empty DelugeHash are
						// indexed. Backs GetBookFilesNeedingDelugeImport
						// (deluge discovery handler + centralization plugin)
						// so they don't have to scan all 308K BookFiles to
						// find the small subset that came from Deluge.
						Name:         memIdxDelugeHash,
						AllowMissing: true,
						Indexer:      &nonEmptyStringFieldIndex{Field: "DelugeHash"},
					},
				},
			},

			memTableNarrators: {
				Name: memTableNarrators,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.IntFieldIndex{Field: "ID"},
					},
					memIdxName: {
						// NOT unique. Pebble may legitimately have multiple
						// Narrator rows with the same name (case-insensitive
						// matching is best-effort, not enforced). Unique would
						// abort warmup on the first collision.
						// AllowMissing covers empty-name rows.
						Name:         memIdxName,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "Name", Lowercase: true},
					},
				},
			},

			memTableBookAuthors: {
				Name: memTableBookAuthors,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						// Composite primary key: book_id + author_id
						Name:   memIdxID,
						Unique: true,
						Indexer: &memdb.CompoundIndex{
							Indexes: []memdb.Indexer{
								&memdb.StringFieldIndex{Field: "BookID"},
								&memdb.IntFieldIndex{Field: "AuthorID"},
							},
						},
					},
					memIdxBookID: {
						Name:    memIdxBookID,
						Indexer: &memdb.StringFieldIndex{Field: "BookID"},
					},
					memIdxAuthorID: {
						Name:    memIdxAuthorID,
						Indexer: &memdb.IntFieldIndex{Field: "AuthorID"},
					},
				},
			},

			memTableBookNarrators: {
				Name: memTableBookNarrators,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:   memIdxID,
						Unique: true,
						Indexer: &memdb.CompoundIndex{
							Indexes: []memdb.Indexer{
								&memdb.StringFieldIndex{Field: "BookID"},
								&memdb.IntFieldIndex{Field: "NarratorID"},
							},
						},
					},
					memIdxBookID: {
						Name:    memIdxBookID,
						Indexer: &memdb.StringFieldIndex{Field: "BookID"},
					},
					memIdxNarratorID: {
						Name:    memIdxNarratorID,
						Indexer: &memdb.IntFieldIndex{Field: "NarratorID"},
					},
				},
			},

			memTableImportPaths: {
				Name: memTableImportPaths,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.IntFieldIndex{Field: "ID"},
					},
					memIdxPath: {
						// NOT unique (warmup-safety). Pebble already de-dupes
						// import paths at write time via path-keyed lookup, but
						// historical data may have collisions and we don't want
						// warmup to abort.
						Name:         memIdxPath,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "Path"},
					},
					memIdxEnabled: {
						Name:    memIdxEnabled,
						Indexer: &memdb.BoolFieldIndex{Field: "Enabled"},
					},
				},
			},

			memTableAuthorAliases: {
				Name: memTableAuthorAliases,
				Indexes: map[string]*memdb.IndexSchema{
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.IntFieldIndex{Field: "ID"},
					},
					memIdxAuthorID: {
						Name:    memIdxAuthorID,
						Indexer: &memdb.IntFieldIndex{Field: "AuthorID"},
					},
					memIdxAliasName: {
						// AllowMissing: legacy rows may have empty AliasName.
						Name:         memIdxAliasName,
						AllowMissing: true,
						Indexer:      &memdb.StringFieldIndex{Field: "AliasName", Lowercase: true},
					},
				},
			},

			memTableBlockedHashes: {
				Name: memTableBlockedHashes,
				Indexes: map[string]*memdb.IndexSchema{
					// go-memdb requires every table to have an "id" index.
					memIdxID: {
						Name:    memIdxID,
						Unique:  true,
						Indexer: &memdb.StringFieldIndex{Field: "Hash"},
					},
					memIdxHash: {
						Name:    memIdxHash,
						Unique:  true,
						Indexer: &memdb.StringFieldIndex{Field: "Hash"},
					},
				},
			},

		},
	}
}
