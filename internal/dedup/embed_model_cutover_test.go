// file: internal/dedup/embed_model_cutover_test.go
// version: 1.1.0
// guid: 8a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9
// last-edited: 2026-07-03

package dedup

import (
	"context"
	"strconv"
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

// TestEmbedAuthor_ReembedsOnModelChange locks the DEDUPC-1 fix: EmbedAuthor's
// cached-skip must also require the stored model to match the wired client's
// model, mirroring prepBookEmbed / TestEmbedBooks_ReembedsOnModelChange above.
// Author names almost never change, so without this guard every author
// embedding stays permanently stranded on whatever model created it after a
// backend cutover (e.g. OpenAI text-embedding-3-large -> local bge-m3),
// silently degrading author dedup Layer 2 to mixed-dimension comparisons.
func TestEmbedAuthor_ReembedsOnModelChange(t *testing.T) {
	engine, mock, es := setupTestEngine(t)

	const authorID = 42
	entityID := strconv.Itoa(authorID)
	author := &database.Author{ID: authorID, Name: "A Real Author"}
	mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		if id == authorID {
			return author, nil
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
	if err := engine.EmbedAuthor(context.Background(), authorID); err != nil {
		t.Fatalf("first EmbedAuthor: %v", err)
	}
	first, _ := es.Get("author", entityID)
	if first == nil || first.Model != "model-A" {
		t.Fatalf("after first embed, stored model = %v, want model-A", first)
	}

	// Switch the client to model-B. Content hash is UNCHANGED (author name did
	// not change), so the only reason to re-embed is the model change. Before
	// the fix this was skipped (cached).
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
	if err := engine.EmbedAuthor(context.Background(), authorID); err != nil {
		t.Fatalf("second EmbedAuthor: %v", err)
	}
	if bCalls == 0 {
		t.Error("expected a re-embed API call after model change, but the author was skipped (cached)")
	}
	second, _ := es.Get("author", entityID)
	if second == nil || second.Model != "model-B" {
		t.Fatalf("after model change, stored model = %v, want model-B (re-embed)", second)
	}

	// Sanity: re-running at the SAME model with unchanged content DOES skip.
	bCalls = 0
	if err := engine.EmbedAuthor(context.Background(), authorID); err != nil {
		t.Fatalf("third EmbedAuthor: %v", err)
	}
	if bCalls != 0 {
		t.Errorf("expected a cached skip when model+content unchanged, but re-embedded (%d calls)", bCalls)
	}
}
