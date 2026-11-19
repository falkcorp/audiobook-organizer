// file: internal/plugins/dedup/kinds_embedding.go
// version: 1.0.0
// guid: 6209a7e9-41c2-4ab7-bf95-030d41c7eef1
// last-edited: 2026-11-18

package dedup

import (
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The embedding group (F5-04): the definitions that embed books and authors,
// re-embed or re-encode the corpus, and clean orphaned embedding rows.

// embeddingKindStore is the reads the embedding group's cores take, composed by
// embedding each definition's own narrow interface (the F5-11 precedent): a core
// takes only its own piece, the composition is what EmbeddingKindDeps carries.
type embeddingKindStore interface {
	cleanupOrphanEmbeddingsStore
}

// embeddingKindWriter is the writes the embedding group's cores make, composed
// the same way.
type embeddingKindWriter interface {
	cleanupOrphanEmbeddingsWriter
}

// EmbeddingKindDeps is what the embedding group's native kinds need from their
// owner. F5-18 wires them: the stores behind Store, the gated writer behind
// NewWriter; tests pass fakes.
type EmbeddingKindDeps struct {
	Store     embeddingKindStore
	NewWriter func(tasks.Writer) embeddingKindWriter
}

// embeddingWriterFor builds the run's writer over its kernel-gated tasks.Writer,
// refusing a run whose owner declared no factory (or one that returned nothing).
func embeddingWriterFor(kind tasks.Kind, d EmbeddingKindDeps, rc *tasks.RC) (embeddingKindWriter, error) {
	if err := errNeed(kind, "Embedding.NewWriter", d.NewWriter != nil); err != nil {
		return nil, err
	}
	w := d.NewWriter(rc.Writer())
	if w == nil {
		return nil, errNeedWriter(kind)
	}
	return w, nil
}

// headEmbeddingReads is HEAD's reads for the embedding cores: the embedding
// store and the main store the HEAD body read through p.embeddingStore and p.store.
type headEmbeddingReads struct {
	emb   *database.EmbeddingStore
	books pluginBookReader
}

func (h headEmbeddingReads) ListByType(entityType string) ([]database.Embedding, error) {
	return h.emb.ListByType(entityType)
}

func (h headEmbeddingReads) GetBookByID(id string) (*database.Book, error) {
	return h.books.GetBookByID(id)
}

// headEmbeddingWriter is HEAD's writes for the embedding cores: each method
// calls exactly what the HEAD body called (rule R10 item 5).
type headEmbeddingWriter struct{ emb *database.EmbeddingStore }

func (h headEmbeddingWriter) Delete(entityType, entityID string) error {
	return h.emb.Delete(entityType, entityID)
}

// embeddingKinds lists the embedding group's native kinds in HEAD file order.
func embeddingKinds(p *Plugin, d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return []tasks.Definition{
		p.cleanupOrphanEmbeddingsNative(d, sched),
	}
}

// cleanupOrphanEmbeddingsNative is the native dedup.cleanup-orphan-embeddings
// kind. Preview is HEAD's dry run (apply=false: report only); Live deletes the
// rows the scan proved orphaned.
func (p *Plugin) cleanupOrphanEmbeddingsNative(d EmbeddingKindDeps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := kindCommon(cleanupOrphanEmbeddingsKind, sched, false)
	c.Title = "Clean up orphaned book embeddings"
	c.Help = cleanupOrphanEmbeddingsHelp
	// Writes found: the delete pass calls EmbeddingStore.Delete, which removes
	// emb:v:book:<id> rows (embeddings). The scan reads books and writes nothing.
	c.Effects = tasks.Writes(res(ops.ResEmbeddings)...).AlsoReads(res(ops.ResBooks)...)
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityNormal
	c.ConcurrencyKey = tasks.Key("books.dedup.cleanup-orphan-embeddings")
	c.Timeout = 30 * time.Minute
	return tasks.Task(cleanupOrphanEmbeddingsKind, tasks.TaskSpec[json.RawMessage, cleanupOrphanEmbeddingsResult]{
		Common: c,
		Run: func(rc *tasks.RC, raw json.RawMessage) (cleanupOrphanEmbeddingsResult, error) {
			if err := refuseKindModeKeys(cleanupOrphanEmbeddingsKind, raw); err != nil {
				return cleanupOrphanEmbeddingsResult{}, err
			}
			if err := errNeed(cleanupOrphanEmbeddingsKind, "Embedding.Store", d.Store != nil); err != nil {
				return cleanupOrphanEmbeddingsResult{}, err
			}
			env := cleanupOrphanEmbeddingsEnv{kindEnv: nativeEnv(rc), Store: d.Store}
			if env.Live {
				w, err := embeddingWriterFor(cleanupOrphanEmbeddingsKind, d, rc)
				if err != nil {
					return cleanupOrphanEmbeddingsResult{}, err
				}
				env.Writer = w
			}
			return cleanupOrphanEmbeddingsCore(rc.Context(), env)
		},
	})
}
