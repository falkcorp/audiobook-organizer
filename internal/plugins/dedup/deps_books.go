// file: internal/plugins/dedup/deps_books.go
// version: 0.1.0
// guid: 7a3c9e15-d2b8-4f61-8e04-5c1b6a9d7f32
// last-edited: 2026-11-28

package dedup

import (
	"context"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's KindDeps for the dedup kinds (F5-18 A6d). One read value
// serves every group's Store; one writer value serves every group's NewWriter
// (the groups' writer interfaces never disagree on a method's signature, except
// the split-book candidate store's Delete, which has its own writer).
//
// They do not build on the head* adapters: those are deleted with the v2
// definitions, and a builder that reused one would make that deletion a rewrite.
// Each write calls exactly what the HEAD adapter called.

// booksKindRows is the rows of the writers' methods. The shared table has
// SetSetting and ModifyBook; the rest follow THE RULE of resources.Table (every
// resource the call can write, through its whole chain), checked against the
// store code, and agree with what each kind's own Effects declare:
//
//   - the dedup candidate and label rows, the emb:v: rows and the dedup:s: status
//     index are embeddings (seams.d/F5-04.md);
//   - the four index-built flags and the band thresholds are setting: rows
//     (SetISBNIndexBuilt, SetWindowIndexBuilt and SetLSHIndexBuilt call SetSetting;
//     SetCandidateStatusIndexBuilt writes the setting: key itself,
//     internal/books/store/embedding_store.go:994) and the config write: settings;
//   - the ISBN/ASIN index rows are books, as build-isbn-index declares; the
//     fpwinidx: and fpidx: rows are fingerprint_index; the segment sweep rewrites
//     book_file: rows;
//   - EnsureSinglePrimary reaches its store through ModifyBook alone
//     (versionprimary.EnsureStore), so it is the ModifyBook row;
//   - ReloadScoreConfig is the engine's re-band (Rescore apply=true), whose only
//     write is UpdateCandidateScores: embeddings;
//   - PersistedScoreConfig only reads the config snapshot: an empty row, so it
//     stays outside the fence as a read does;
//   - the split-book candidate rows are split_book_candidates, and the merge is
//     the bulk-merge kind's declared set.
func booksKindRows() (writer.Rows, error) {
	shared := writer.SharedRows()
	rows := writer.Rows{}
	for _, m := range []string{"SetSetting", "ModifyBook"} {
		row, ok := shared[m]
		if !ok {
			return nil, fmt.Errorf("dedup: no resource row for %s", m)
		}
		rows[m] = row
	}
	one := func(r ops.Resource) []ops.Resource { return []ops.Resource{r} }
	rows["EnsureSinglePrimary"] = rows["ModifyBook"]
	for _, m := range []string{
		"DeleteEmbedding", "ReclassifyCandidate", "ReencodeRows", "SyncCandidateWrites",
		"UpdateCandidateScores", "UpsertLabeledExample", "DeleteLabeledExamplesBySource",
		"WriteCandidateStatusIndexRow", "ReloadScoreConfig",
	} {
		rows[m] = one(ops.ResEmbeddings)
	}
	for _, m := range []string{
		"SetCandidateStatusIndexBuilt", "SetISBNIndexBuilt", "SetWindowIndexBuilt",
		"SetLSHIndexBuilt", "PersistBandThresholds",
	} {
		rows[m] = one(ops.ResSettings)
	}
	rows["WriteISBNIndexForBook"] = one(ops.ResBooks)
	rows["PutWindowIndex"] = one(ops.ResFingerprintIndex)
	rows["PutLSHEntries"] = one(ops.ResFingerprintIndex)
	rows["SweepBookFileSegDrop"] = one(ops.ResBookFiles)
	rows["PersistedScoreConfig"] = nil
	rows["SaveSplitBookCandidates"] = one(ops.ResSplitBookCandidates)
	rows["DeleteSplitBookCandidate"] = one(ops.ResSplitBookCandidates)
	rows["MergeSplitBookClusterRechecked"] = []ops.Resource{
		ops.ResBooks, ops.ResBookFiles, ops.ResAuthors, ops.ResMetadataCache,
		ops.ResIndexes, ops.ResExternalIDs, ops.ResUserState, ops.ResSyncIdentity, ops.ResOperations,
		ops.ResSplitBookCandidates,
	}
	return rows, nil
}

// newBooksKindDeps is the whole KindDeps of the plugin.
func newBooksKindDeps(in BooksTaskInputs) (KindDeps, error) {
	rows, err := booksKindRows()
	if err != nil {
		return KindDeps{}, err
	}
	reads := booksReads{src: in.Source, idx: in.Indexes, emb: in.Embeddings}
	newWriter := func(w tasks.Writer) *booksWriter {
		return &booksWriter{src: in.Source, idx: in.Indexes, emb: in.Embeddings, eng: in.Engine,
			gate: newBooksGate(w, rows)}
	}
	newSplit := func(w tasks.Writer) splitBookKindWriter {
		db := in.Embeddings.PebbleDB()
		if db == nil {
			return nil // errNeedWriter: HEAD's "shared Pebble DB unavailable"
		}
		return &booksSplitWriter{src: in.Source, cands: dedupengine.NewSplitBookStore(db),
			gate: newBooksGate(w, rows)}
	}
	return KindDeps{
		Scoring: ScoringKindDeps{
			Store:          reads,
			NewEngine:      func(w tasks.Writer) scoringEngine { return in.NewEngine(w) },
			NewWriter:      func(w tasks.Writer) scoringKindWriter { return newWriter(w) },
			SplitReads:     reads,
			NewSplitWriter: newSplit,
		},
		Embedding: EmbeddingKindDeps{
			Store:          reads,
			NewWriter:      func(w tasks.Writer) embeddingKindWriter { return newWriter(w) },
			NewEngine:      func(w tasks.Writer) embeddingKindEngine { return in.NewEngine(w) },
			EmbeddingModel: in.Engine.EmbeddingModel,
		},
		Labels: LabelKindDeps{
			Store:     reads,
			Scorer:    in.Engine,
			Baseline:  in.Engine,
			NewWriter: func(w tasks.Writer) labelKindWriter { return newWriter(w) },
			NewEngine: func(w tasks.Writer) llmReviewEngine { return in.NewEngine(w) },
		},
		Index: IndexKindDeps{
			Store:     reads,
			NewWriter: func(w tasks.Writer) indexKindWriter { return newWriter(w) },
		},
		Chain: ChainKindDeps{
			LSHReads:     reads,
			NewLSHWriter: func(w tasks.Writer) lshIndexBuildWriter { return newWriter(w) },
			RescorePreview: func(ctx context.Context) (dedupengine.RescoreResult, error) {
				return in.Engine.Rescore(ctx, false)
			},
		},
	}, nil
}

// booksReads is every group's read store: the library store, the index
// capabilities and the embedding store.
type booksReads struct {
	src Source
	idx IndexSource
	emb *database.EmbeddingStore
}

var (
	_ scoringKindStore   = booksReads{}
	_ embeddingKindStore = booksReads{}
	_ indexKindStore     = booksReads{}
	_ labelKindStore     = booksReads{}
	_ splitBookKindReads = booksReads{}
	_ lshIndexBuildReads = booksReads{}
)

func (r booksReads) GetAllAuthors() ([]database.Author, error) { return r.src.GetAllAuthors() }
func (r booksReads) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return r.src.GetAllBooksCore(limit, offset)
}
func (r booksReads) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return r.src.GetAllBookFilesCore()
}
func (r booksReads) GetBookByID(id string) (*database.Book, error) { return r.src.GetBookByID(id) }
func (r booksReads) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return r.src.GetBookFiles(bookID)
}
func (r booksReads) GetSetting(key string) (*database.Setting, error) { return r.src.GetSetting(key) }

// LSHIndexBuilt: the capability is required input, so it is always available.
func (r booksReads) LSHIndexBuilt() (built, available bool) { return r.idx.IsLSHIndexBuilt(), true }
func (r booksReads) HasLSHIndex(fileID string) bool         { return r.idx.HasLSHIndex(fileID) }
func (r booksReads) IsISBNIndexBuilt() bool                 { return r.idx.IsISBNIndexBuilt() }
func (r booksReads) ListWindowIndexFiles() ([]string, error) {
	return r.idx.ListWindowIndexFiles()
}
func (r booksReads) WindowIndexMembers(fileID string) ([]fingerprint.WindowBand, bool, error) {
	return r.idx.WindowIndexMembers(fileID)
}
func (r booksReads) SweepBookFileSegDropDry(ctx context.Context, batchSize int, progress func(rewrite, total int)) (database.SweepBookFileSegDropResult, error) {
	return r.idx.SweepBookFileSegDrop(ctx, true, batchSize, progress)
}

func (r booksReads) ListCandidates(f database.CandidateFilter) ([]database.DedupCandidate, int, error) {
	return r.emb.ListCandidates(f)
}
func (r booksReads) GetCandidateByID(id int64) (*database.DedupCandidate, error) {
	return r.emb.GetCandidateByID(id)
}
func (r booksReads) GetLabeledExample(candidateID int64) (*database.LabeledExample, error) {
	return r.emb.GetLabeledExample(candidateID)
}
func (r booksReads) ListLabeledExamples(f database.LabeledExampleFilter) ([]database.LabeledExample, error) {
	return r.emb.ListLabeledExamples(f)
}
func (r booksReads) Get(entityType, entityID string) (*database.Embedding, error) {
	return r.emb.Get(entityType, entityID)
}
func (r booksReads) ListByType(entityType string) ([]database.Embedding, error) {
	return r.emb.ListByType(entityType)
}

// ScanEmbeddingRows iterates the emb:v: rows, as emb-reencode's HEAD read did.
func (r booksReads) ScanEmbeddingRows(fn func(key, value []byte) error) error {
	db := r.emb.PebbleDB()
	if db == nil {
		return errors.New("embedding store PebbleDB handle is nil")
	}
	prefix := []byte("emb:v:") // mirrors the const in embedding_store.go
	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: pebbleUpperBound(prefix)})
	if err != nil {
		return fmt.Errorf("emb-reencode: open iterator: %w", err)
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		if err := fn(iter.Key(), iter.Value()); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("emb-reencode: scan error: %w", err)
	}
	return nil
}

func (r booksReads) DetectSplitBookCandidates(ctx context.Context, progress func(loaded int)) ([]dedupengine.SplitBookCandidate, error) {
	return dedupengine.DetectSplitBookCandidates(ctx, r.src, progress)
}
func (r booksReads) RecheckSplitBookMembers(bookIDs []string, chapterMaxSec int) ([]string, error) {
	return dedupengine.RecheckSplitBookMembers(r.src, bookIDs, chapterMaxSec)
}

// booksWriter is one run's gated writes for every group. A ReadOnly or Preview
// run has no writer (w is nil): every method with a row then refuses instead of
// dereferencing nil.
type booksWriter struct {
	src  Source
	idx  IndexSource
	emb  *database.EmbeddingStore
	eng  LiveEngine
	gate *writer.Gate
}

var (
	_ scoringKindWriter   = (*booksWriter)(nil)
	_ embeddingKindWriter = (*booksWriter)(nil)
	_ indexKindWriter     = (*booksWriter)(nil)
	_ labelKindWriter     = (*booksWriter)(nil)
	_ lshIndexBuildWriter = (*booksWriter)(nil)
)

func (w *booksWriter) SetSetting(key, value, typ string, secret bool) error {
	return w.gate.Err(context.Background(), "SetSetting", writer.Ref(string(ops.ResSettings), key), func() error {
		return w.src.SetSetting(key, value, typ, secret)
	})
}

func (w *booksWriter) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return writer.Value(context.Background(), w.gate, "ModifyBook", writer.Ref(string(ops.ResBooks), id), func() (*database.Book, error) {
		return w.src.ModifyBook(id, fn)
	})
}

func (w *booksWriter) EnsureSinglePrimary(ctx context.Context, groupID string) error {
	return w.gate.Err(ctx, "EnsureSinglePrimary", writer.Ref(string(ops.ResBooks), groupID), func() error {
		_, err := versionprimary.EnsureSinglePrimary(ctx, w.src, groupID,
			versionprimary.Env{RootDir: config.AppConfig.RootDir})
		return err
	})
}

func (w *booksWriter) Delete(entityType, entityID string) error {
	return w.gate.Err(context.Background(), "DeleteEmbedding", writer.Ref(string(ops.ResEmbeddings), entityType+":"+entityID), func() error {
		return w.emb.Delete(entityType, entityID)
	})
}

func (w *booksWriter) ReclassifyCandidate(id int64, fromStatus, toStatus string) error {
	return w.gate.Err(context.Background(), "ReclassifyCandidate", writer.Ref(string(ops.ResEmbeddings), fmt.Sprint(id)), func() error {
		return w.emb.ReclassifyCandidate(id, fromStatus, toStatus)
	})
}

func (w *booksWriter) UpdateCandidateScores(updates []database.CandidateScoreUpdate) (int, []int64, error) {
	var failed []int64
	n, err := writer.Value(context.Background(), w.gate, "UpdateCandidateScores", writer.Ref(string(ops.ResEmbeddings), ""), func() (int, error) {
		applied, f, e := w.emb.UpdateCandidateScores(updates)
		failed = f
		return applied, e
	})
	return n, failed, err
}

func (w *booksWriter) SyncCandidateWrites() error {
	return w.gate.Err(context.Background(), "SyncCandidateWrites", writer.Ref(string(ops.ResEmbeddings), ""), w.emb.SyncCandidateWrites)
}

func (w *booksWriter) UpsertLabeledExample(ex database.LabeledExample) error {
	return w.gate.Err(context.Background(), "UpsertLabeledExample", writer.Ref(string(ops.ResEmbeddings), fmt.Sprint(ex.CandidateID)), func() error {
		return w.emb.UpsertLabeledExample(ex)
	})
}

func (w *booksWriter) DeleteLabeledExamplesBySource(sources ...string) (int, error) {
	return writer.Value(context.Background(), w.gate, "DeleteLabeledExamplesBySource", writer.Ref(string(ops.ResEmbeddings), ""), func() (int, error) {
		return w.emb.DeleteLabeledExamplesBySource(sources...)
	})
}

func (w *booksWriter) ReencodeRows(chunk []reencodeTarget) error {
	return w.gate.Err(context.Background(), "ReencodeRows", writer.Ref(string(ops.ResEmbeddings), ""), func() error {
		db := w.emb.PebbleDB()
		if db == nil {
			return errors.New("embedding store PebbleDB handle is nil")
		}
		return reencodeChunkOn(db, chunk)
	})
}

func (w *booksWriter) SetCandidateStatusIndexBuilt() error {
	return w.gate.Err(context.Background(), "SetCandidateStatusIndexBuilt", writer.Ref(string(ops.ResSettings), ""), w.emb.SetCandidateStatusIndexBuilt)
}

func (w *booksWriter) WriteCandidateStatusIndexRow(id int64, status string) error {
	return w.gate.Err(context.Background(), "WriteCandidateStatusIndexRow", writer.Ref(string(ops.ResEmbeddings), fmt.Sprint(id)), func() error {
		return w.emb.WriteCandidateStatusIndexRow(id, status)
	})
}

func (w *booksWriter) SetISBNIndexBuilt() error {
	return w.gate.Err(context.Background(), "SetISBNIndexBuilt", writer.Ref(string(ops.ResSettings), ""), w.idx.SetISBNIndexBuilt)
}

func (w *booksWriter) WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error {
	return w.gate.Err(context.Background(), "WriteISBNIndexForBook", writer.Ref(string(ops.ResBooks), bookID), func() error {
		return w.idx.WriteISBNIndexForBook(bookID, isbn10, isbn13, asin)
	})
}

func (w *booksWriter) SetWindowIndexBuilt() error {
	return w.gate.Err(context.Background(), "SetWindowIndexBuilt", writer.Ref(string(ops.ResSettings), ""), w.idx.SetWindowIndexBuilt)
}

func (w *booksWriter) PutWindowIndex(fileID string) (int, error) {
	return writer.Value(context.Background(), w.gate, "PutWindowIndex", writer.Ref(string(ops.ResFingerprintIndex), fileID), func() (int, error) {
		return w.idx.PutWindowIndex(fileID)
	})
}

func (w *booksWriter) SetLSHIndexBuilt() error {
	return w.gate.Err(context.Background(), "SetLSHIndexBuilt", writer.Ref(string(ops.ResSettings), ""), w.idx.SetLSHIndexBuilt)
}

func (w *booksWriter) PutLSHEntries(fileID, bookID string, subs []fingerprint.Subprint, bands []byte) error {
	return w.gate.Err(context.Background(), "PutLSHEntries", writer.Ref(string(ops.ResFingerprintIndex), fileID), func() error {
		return w.idx.PutLSHEntries(fileID, bookID, subs, bands)
	})
}

func (w *booksWriter) SweepBookFileSegDrop(ctx context.Context, batchSize int, progress func(rewrite, total int)) (database.SweepBookFileSegDropResult, error) {
	return writer.Value(ctx, w.gate, "SweepBookFileSegDrop", writer.Ref(string(ops.ResBookFiles), ""), func() (database.SweepBookFileSegDropResult, error) {
		return w.idx.SweepBookFileSegDrop(ctx, false, batchSize, progress)
	})
}

// PersistBandThresholds stores the four band mins as one write through the
// config Facade as the process's own actor (config.MutateChecked), not
// Facade.Update, whose hooks would queue a dedup.rescore of their own.
func (w *booksWriter) PersistBandThresholds(ctx context.Context, cfg unified.ScoreConfig) error {
	return w.gate.Err(ctx, "PersistBandThresholds", writer.Ref(string(ops.ResSettings), ""), func() error {
		return config.MutateChecked(func(c *config.Config) {
			c.Dedup.Signals.BandCertainMin = cfg.BandCertainMin
			c.Dedup.Signals.BandHighMin = cfg.BandHighMin
			c.Dedup.Signals.BandMediumMin = cfg.BandMediumMin
			c.Dedup.Signals.BandReviewMin = cfg.BandReviewMin
		})
	})
}

// PersistedScoreConfig reads the config snapshot (an empty row: a read).
func (w *booksWriter) PersistedScoreConfig() (unified.ScoreConfig, error) {
	var cfg unified.ScoreConfig
	err := w.gate.Err(context.Background(), "PersistedScoreConfig", writer.Ref(string(ops.ResSettings), ""), func() error {
		var e error
		cfg, e = config.Snapshot().Dedup.Signals.ScoreConfig()
		return e
	})
	return cfg, err
}

func (w *booksWriter) ReloadScoreConfig(ctx context.Context, cfg unified.ScoreConfig) (dedupengine.RescoreResult, error) {
	return writer.Value(ctx, w.gate, "ReloadScoreConfig", writer.Ref(string(ops.ResEmbeddings), ""), func() (dedupengine.RescoreResult, error) {
		return w.eng.ReloadScoreConfig(ctx, cfg)
	})
}

// booksSplitWriter is the split-book kinds' gated writes: the candidate rows and
// the rechecked merge.
type booksSplitWriter struct {
	src   Source
	cands *dedupengine.SplitBookStore
	gate  *writer.Gate
}

var _ splitBookKindWriter = (*booksSplitWriter)(nil)

func (w *booksSplitWriter) SaveAll(cands []dedupengine.SplitBookCandidate) error {
	return w.gate.Err(context.Background(), "SaveSplitBookCandidates", writer.Ref(string(ops.ResSplitBookCandidates), ""), func() error {
		return w.cands.SaveAll(cands)
	})
}

func (w *booksSplitWriter) Delete(candidateID string) error {
	return w.gate.Err(context.Background(), "DeleteSplitBookCandidate", writer.Ref(string(ops.ResSplitBookCandidates), candidateID), func() error {
		return w.cands.Delete(candidateID)
	})
}

func (w *booksSplitWriter) MergeSplitBookClusterRechecked(bookIDs []string, keepID, suggestedTitle, opID string) (*dedupengine.SplitBookMergeResult, error) {
	return writer.Value(context.Background(), w.gate, "MergeSplitBookClusterRechecked", writer.Ref(string(ops.ResBooks), keepID), func() (*dedupengine.SplitBookMergeResult, error) {
		return dedupengine.MergeSplitBookClusterRechecked(w.src, bookIDs, keepID, suggestedTitle,
			dedupengine.SplitMergeOptions{OperationID: opID})
	})
}
