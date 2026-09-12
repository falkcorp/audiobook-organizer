// file: internal/server/handlers/metadata/scan_standdown_renew_test.go
// version: 1.0.0
// guid: 6b2d8f41-9e37-4c05-a1f8-3d7e0b9c5a26
// last-edited: 2026-09-12

package metadatahandler_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// renewGate grants the request hold and counts renewals and releases; flip
// renewFail to model a lapsed lease.
type renewGate struct {
	renewFail        atomic.Bool
	renews, released atomic.Int32
}

func (g *renewGate) TryAcquireScanStandDown(string, string) (func(), error) {
	return func() { g.released.Add(1) }, nil
}

func (g *renewGate) RenewScanStandDown(string) bool {
	g.renews.Add(1)
	return !g.renewFail.Load()
}

func TestBulkFetchMetadata_RenewsHoldPerBook(t *testing.T) {
	h, d := newHandler(t)
	g := &renewGate{}
	h.SetScanStandDownGate(g)
	d.store.EXPECT().GetBookByID("b1").Return(nil, nil)
	d.store.EXPECT().GetBookByID("b2").Return(nil, nil)
	w := doReq(h.BulkFetchMetadata, http.MethodPost, "/metadata/bulk-fetch",
		map[string]any{"book_ids": []string{"b1", "b2"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if n := g.renews.Load(); n != 2 {
		t.Fatalf("renewals = %d, want one per book (2)", n)
	}
	if n := g.released.Load(); n != 1 {
		t.Fatalf("released = %d, want 1", n)
	}
}

// Once the hold is lost no book is read or written: the strict store mock has
// no expectations, so any GetBookByID/UpdateBook fails the test.
func TestBulkFetchMetadata_LostHoldWritesNothing(t *testing.T) {
	h, _ := newHandler(t)
	g := &renewGate{}
	g.renewFail.Store(true)
	h.SetScanStandDownGate(g)
	w := doReq(h.BulkFetchMetadata, http.MethodPost, "/metadata/bulk-fetch",
		map[string]any{"book_ids": []string{"b1", "b2"}}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), opsregistry.ErrScanStandDownLost.Error()) {
		t.Fatalf("results do not name the lost hold: %s", w.Body.String())
	}
	if n := g.released.Load(); n != 1 {
		t.Fatalf("released = %d, want 1", n)
	}
}

func expectApply(d testDeps) {
	d.mfs.EXPECT().ApplyMetadataCandidate("b1", mock.Anything, mock.Anything).
		Return(&metafetch.FetchMetadataResponse{Message: "applied", Source: "audible", Book: &database.Book{ID: "b1"}}, nil)
	d.mfs.EXPECT().InvalidateCachedCandidates("b1").Return(nil)
	d.wb.EXPECT().Enqueue("b1").Return()
	d.store.EXPECT().GetBookByID("b1").Return(&database.Book{ID: "b1", Title: "T"}, nil)
}

// The single apply's background file job keeps the hold after the response is
// written, renews it when it runs, and writes nothing once it is lost. The
// strict fetch-service mock has no ApplyMetadataFileIO/WriteBack expectation.
func TestApplyAudiobookMetadata_BackgroundJobKeepsHoldAndStopsWhenLost(t *testing.T) {
	h, d := newHandler(t)
	g := &renewGate{}
	h.SetScanStandDownGate(g)
	expectApply(d)
	var job func()
	d.pool.EXPECT().Submit("b1", mock.Anything).Run(func(_ string, fn func()) { job = fn }).Return(true)
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		map[string]any{"candidate": map[string]any{"title": "X"}, "fields": []string{"title"}}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if n := g.released.Load(); n != 0 {
		t.Fatalf("hold released before the queued file job ran (released=%d)", n)
	}
	g.renewFail.Store(true)
	job()
	if n := g.renews.Load(); n < 1 {
		t.Fatal("the file job never renewed the hold")
	}
	if n := g.released.Load(); n != 1 {
		t.Fatalf("released = %d after the job, want 1", n)
	}
}

// A job the pool drops (stopped) never runs, so the handler releases its share.
func TestApplyAudiobookMetadata_DroppedJobReleasesHold(t *testing.T) {
	h, d := newHandler(t)
	g := &renewGate{}
	h.SetScanStandDownGate(g)
	expectApply(d)
	d.pool.EXPECT().Submit("b1", mock.Anything).Return(false)
	w := doReq(h.ApplyAudiobookMetadata, http.MethodPost, "/audiobooks/b1/apply-metadata",
		map[string]any{"candidate": map[string]any{"title": "X"}, "fields": []string{"title"}}, idParam("b1"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if n := g.released.Load(); n != 1 {
		t.Fatalf("released = %d, want 1 (the dropped job must not strand the hold)", n)
	}
}
