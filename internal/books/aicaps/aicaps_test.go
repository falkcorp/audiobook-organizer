// file: internal/books/aicaps/aicaps_test.go
// version: 0.1.0
// guid: 82efc133-46bb-4fca-847a-9a797e179608
// last-edited: 2026-11-20

package aicaps_test

import (
	"context"
	"errors"
	"iter"
	"maps"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/aidispatch"
	"github.com/falkcorp/audiobook-organizer/internal/books/aicaps"
	"github.com/falkcorp/audiobook-organizer/internal/books/taskmanage"
	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The stubs below give ai.NewRouter the Registry it insists on. A catalog test
// never reaches an instance, so none of them is ever called.
type noStore struct{}

func (noStore) Load(context.Context) ([]integrations.StoredInstance, error) { return nil, nil }
func (noStore) Put(context.Context, *storage.Batch, integrations.StoredInstance) (uint64, error) {
	return 0, errors.New("unused")
}
func (noStore) Delete(context.Context, *storage.Batch, integrations.Ref) error {
	return errors.New("unused")
}

type noSecrets struct{}

func (noSecrets) Get(context.Context, secrets.Name) (secrets.Value, secrets.Meta, error) {
	return secrets.Value{}, secrets.Meta{}, errors.New("unused")
}
func (noSecrets) Meta(context.Context, secrets.Name) (secrets.Meta, error) {
	return secrets.Meta{}, errors.New("unused")
}
func (noSecrets) Stage(context.Context, *storage.Batch, secrets.Write) error {
	return errors.New("unused")
}

type noAudit struct{}

func (noAudit) RecordIn(context.Context, *storage.Batch, audit.Event) error { return nil }

// newRouter is a real ai.Router over a Registry that holds nothing.
func newRouter(t testing.TB) *ai.Router {
	t.Helper()
	reg, err := integrations.New(integrations.Options{
		Store: noStore{}, NewBatch: func() *storage.Batch { return nil }, Secrets: noSecrets{}, Audit: noAudit{},
	})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	r, err := ai.NewRouter(ai.RouterOptions{Registry: reg})
	if err != nil {
		t.Fatalf("router: %v", err)
	}
	return r
}

// wantIDs is the catalog's 13 IDs, written out (05-ai section 10): a change to
// the spec's list is a failing test here, not a drifted constant.
var wantIDs = []string{
	"books.filename_parse", "books.audiobook_parse", "books.cover_art_vision",
	"books.author_review", "books.author_discovery", "books.metadata_rerank",
	"books.dedup_review", "books.author_dedup_batch", "books.diagnostics",
	"books.embed_text", "books.embed_text_batch",
	"books.transcribe_batch", "books.transcribe_intro_clip",
}

func TestSpecs_ThirteenIDsInHEADOrder(t *testing.T) {
	var got []string
	for _, s := range aicaps.Specs() {
		got = append(got, s.ID)
	}
	if !slices.Equal(got, wantIDs) {
		t.Fatalf("catalog IDs\n got %v\nwant %v", got, wantIDs)
	}
}

// Every spec goes through the real catalog as module "books", and all 13
// register. books.embed_text_batch requires batch_api of embedding work, which
// core's featureFitsKind accepts since the 2026-11-22 ruling
// (TODO-F7-09-EMBED-BATCH-FEATURE).
func TestSpecs_AllThirteenRegisterUnderBooks(t *testing.T) {
	r := newRouter(t)
	for _, s := range aicaps.Specs() {
		if _, err := r.RegisterCapability(aicaps.Module, s); err != nil {
			t.Errorf("%s: %v", s.ID, err)
		}
	}
	r.FreezeCatalog()
	var got []string
	for e := range r.Catalog() {
		got = append(got, e.ID)
	}
	slices.Sort(got)
	want := slices.Clone(wantIDs)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("catalog holds %d capabilities\n got %v\nwant %v", len(got), got, want)
	}
}

// Specs returns a fresh slice: a caller that edits one cannot change the next.
func TestSpecs_FreshCopyPerCall(t *testing.T) {
	a := aicaps.Specs()
	a[0].ID = "books.changed"
	a[1].RequiredFeatures = append(a[1].RequiredFeatures, ai.FeatureTools)
	b := aicaps.Specs()
	if b[0].ID != wantIDs[0] || slices.Contains(b[1].RequiredFeatures, ai.FeatureTools) {
		t.Fatal("Specs shares state between calls")
	}
}

// Each spec carries what HEAD's capability carried: its kind, its required and
// optional features, and its three texts. The expectations are HEAD's own
// registry, read through aidispatch.Registry, mapped by the documented rules
// (chat is text, whisper is transcribe, batch_api is batch).
func TestSpecs_CarryHEADRegistrations(t *testing.T) {
	head := map[string]aidispatch.Spec{}
	for _, s := range aidispatch.Registry() {
		head[s.Capability.ID()] = s
	}
	if len(head) != 13 {
		t.Fatalf("HEAD registers %d capabilities, this test was written for 13", len(head))
	}
	kinds := map[aidispatch.Kind]ai.Kind{aidispatch.KindChat: ai.KindText, aidispatch.KindEmbed: ai.KindEmbed, aidispatch.KindWhisper: ai.KindTranscribe}
	feats := map[string]ai.Feature{aidispatch.FeatureBatchAPI: ai.FeatureBatch, aidispatch.FeatureVision: ai.FeatureVision}
	for _, s := range aicaps.Specs() {
		headID, ok := aicaps.HEADID(s.ID)
		if !ok {
			t.Errorf("%s has no HEAD ID", s.ID)
			continue
		}
		h, ok := head[headID]
		if !ok {
			t.Errorf("%s maps to %q, which HEAD does not register", s.ID, headID)
			continue
		}
		if kinds[h.Kind] != s.Kind {
			t.Errorf("%s: kind %q, HEAD's %q is %q", s.ID, s.Kind, h.Kind, kinds[h.Kind])
		}
		var req []ai.Feature
		for _, f := range h.RequiredFeatures {
			req = append(req, feats[f])
		}
		if !slices.Equal(req, s.RequiredFeatures) {
			t.Errorf("%s: required features %v, HEAD's are %v", s.ID, s.RequiredFeatures, req)
		}
		if len(h.OptionalFeatures) != len(s.OptionalFeatures) {
			t.Errorf("%s: optional features %v, HEAD's are %v", s.ID, s.OptionalFeatures, h.OptionalFeatures)
		}
		if s.Description != h.Description || s.DataSent != h.DataSent || s.TypicalRequest != h.TypicalRequest {
			t.Errorf("%s: a disclosure text differs from HEAD's", s.ID)
		}
		if s.Locality != ai.AnyLocality {
			t.Errorf("%s: locality floor %d; 05-ai section 10 imposes none", s.ID, s.Locality)
		}
	}
}

// The HEAD-to-books map is HEAD's 13 IDs, each to a distinct catalog ID, and it
// agrees with the rule M6's own copy applies: llm.* moves under books, the other
// four are renamed one by one.
func TestHEADIDMap_ThirteenToThirteen(t *testing.T) {
	want := map[string]string{
		"llm.filename_parse": "books.filename_parse", "llm.audiobook_parse": "books.audiobook_parse",
		"llm.cover_art_vision": "books.cover_art_vision", "llm.author_review": "books.author_review",
		"llm.author_discovery": "books.author_discovery", "llm.metadata_rerank": "books.metadata_rerank",
		"llm.dedup_review": "books.dedup_review", "llm.author_dedup_batch": "books.author_dedup_batch",
		"llm.diagnostics": "books.diagnostics",
		"embed.text":      "books.embed_text", "embed.text_batch": "books.embed_text_batch",
		"transcribe.batch": "books.transcribe_batch", "transcribe.intro_clip": "books.transcribe_intro_clip",
	}
	got := aicaps.HEADToBooks()
	if !maps.Equal(got, want) {
		t.Fatalf("map\n got %v\nwant %v", got, want)
	}
	for _, c := range aidispatch.Registry() {
		if _, ok := got[c.Capability.ID()]; !ok {
			t.Errorf("HEAD registers %s, the map has no row for it", c.Capability.ID())
		}
	}
	for head, id := range got {
		back, ok := aicaps.HEADID(id)
		if !ok || back != head {
			t.Errorf("HEADID(%s) = %q, %v; want %q", id, back, ok, head)
		}
		if aicaps.BooksID(head) != id {
			t.Errorf("BooksID(%s) = %q, want %q", head, aicaps.BooksID(head), id)
		}
	}
	if got := aicaps.BooksID("llm.from_a_newer_build"); got != "llm.from_a_newer_build" {
		t.Errorf("an unknown ID must be kept verbatim, got %q", got)
	}
	if _, ok := aicaps.HEADID("books.nothing"); ok {
		t.Error("HEADID answered for an ID the catalog does not hold")
	}
}

// The job types HEAD registers a callback for become a catalog capability and
// the apply kind the router mints for it. The kind is checked against the
// router's own derivation, not a second copy of the rule.
func TestJobTypes_ApplyKindIsTheRoutersOwn(t *testing.T) {
	rows := aicaps.JobTypes()
	if len(rows) != 2 {
		t.Fatalf("job types %v, want dedup_review and author_dedup_full", rows)
	}
	r := newRouter(t)
	handles := map[string]ai.Capability{}
	for _, s := range aicaps.Specs() {
		c, err := r.RegisterCapability(aicaps.Module, s)
		if err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
		handles[s.ID] = c
	}
	noop := func(*tasks.RC, ai.BatchJob, iter.Seq2[ai.BatchResult, error]) (ai.BatchOutcome, error) {
		return ai.BatchOutcome{}, nil
	}
	for typ, row := range rows {
		c, ok := handles[row.Capability]
		if !ok {
			t.Errorf("%s: capability %s is not in the catalog", typ, row.Capability)
			continue
		}
		def := ai.BatchApplyTask(c, noop, tasks.Writes(tasks.Res("books", "authors")), taskmanage.Requirement())
		if string(def.Kind()) != row.ApplyKind {
			t.Errorf("%s: apply kind %q, the router mints %q", typ, row.ApplyKind, def.Kind())
		}
		if !r.HasApplyKind(row.ApplyKind) {
			t.Errorf("%s: the router does not know apply kind %s", typ, row.ApplyKind)
		}
	}
	want := map[string]aicaps.JobType{
		"dedup_review":      {Capability: "books.dedup_review", ApplyKind: "books.ai.batch-apply.dedup-review"},
		"author_dedup_full": {Capability: "books.author_dedup_batch", ApplyKind: "books.ai.batch-apply.author-dedup-batch"},
	}
	if !maps.Equal(rows, want) {
		t.Errorf("job types\n got %v\nwant %v", rows, want)
	}
}
