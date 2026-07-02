// file: internal/dedup/embed_model_cutover_test.go
// version: 1.0.0
// guid: 8a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9
// last-edited: 2026-07-02

package dedup

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestEmbedBooks_ReembedsOnModelChange locks the fix for the embedding-backend
// cutover bug: prepBookEmbed's cached-skip must also require the stored model to
// match the wired client's model. Otherwise a content-hash hit skips every book
// on a backend switch (e.g. OpenAI text-embedding-3-large -> local bge-m3),
// leaving stale wrong-dimension vectors that score 0 against the new model.
func TestEmbedBooks_ReembedsOnModelChange(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	primary := true
	book := &database.Book{ID: "BOOK_R", Title: "A Real Audiobook", IsPrimaryVersion: &primary}
	mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if id == "BOOK_R" {
			return book, nil
		}
		return nil, nil
	}

	// First embed at model-A.
	clientA := ai.NewEmbeddingClientWithOptions("k", "model-A", "")
	clientA.SetRawEmbedForTest(func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 2, 3, 4}
		}
		return out, nil
	})
	engine.embedClient = clientA
	if _, err := engine.EmbedBooks(context.Background(), []string{"BOOK_R"}); err != nil {
		t.Fatalf("first EmbedBooks: %v", err)
	}
	first, _ := es.Get("book", "BOOK_R")
	if first == nil || first.Model != "model-A" {
		t.Fatalf("after first embed, stored model = %v, want model-A", first)
	}

	// Switch the client to model-B. Content hash is UNCHANGED, so the only reason
	// to re-embed is the model change. Before the fix this was skipped (cached).
	bCalls := 0
	clientB := ai.NewEmbeddingClientWithOptions("k", "model-B", "")
	clientB.SetRawEmbedForTest(func(_ context.Context, texts []string) ([][]float32, error) {
		bCalls++
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{5, 6, 7, 8}
		}
		return out, nil
	})
	engine.embedClient = clientB
	if _, err := engine.EmbedBooks(context.Background(), []string{"BOOK_R"}); err != nil {
		t.Fatalf("second EmbedBooks: %v", err)
	}
	if bCalls == 0 {
		t.Error("expected a re-embed API call after model change, but the book was skipped (cached)")
	}
	second, _ := es.Get("book", "BOOK_R")
	if second == nil || second.Model != "model-B" {
		t.Fatalf("after model change, stored model = %v, want model-B (re-embed)", second)
	}

	// Sanity: re-running at the SAME model with unchanged content DOES skip.
	bCalls = 0
	if _, err := engine.EmbedBooks(context.Background(), []string{"BOOK_R"}); err != nil {
		t.Fatalf("third EmbedBooks: %v", err)
	}
	if bCalls != 0 {
		t.Errorf("expected a cached skip when model+content unchanged, but re-embedded (%d calls)", bCalls)
	}
}
