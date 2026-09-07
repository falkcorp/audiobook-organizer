// file: internal/database/pebble_activity_sources_decode_test.go
// version: 1.0.0
// guid: 3f1c9a52-7d64-4c18-9b0e-2a6f5d81c447
// last-edited: 2026-09-07

package database

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// TestPactEntryNoDetails_MirrorsActivityEntry is the enforcement behind the
// warning on pactEntryNoDetails.
//
// The projection is only safe because matchesFilter reads no field it drops.
// Add a field to ActivityEntry — say a Category that matchesFilter starts
// filtering on — and forget this struct, and the sources scan silently decodes
// it as the zero value: the filter matches the wrong rows and nothing errors.
// A comment cannot catch that. This does.
func TestPactEntryNoDetails_MirrorsActivityEntry(t *testing.T) {
	full := reflect.TypeOf(ActivityEntry{})
	narrow := reflect.TypeOf(pactEntryNoDetails{})

	type fieldSpec struct {
		typ reflect.Type
		tag string
	}
	collect := func(rt reflect.Type) map[string]fieldSpec {
		out := make(map[string]fieldSpec, rt.NumField())
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			out[f.Name] = fieldSpec{typ: f.Type, tag: f.Tag.Get("json")}
		}
		return out
	}

	fullFields, narrowFields := collect(full), collect(narrow)

	// Details, and only Details, may be absent from the projection.
	const dropped = "Details"
	if _, ok := fullFields[dropped]; !ok {
		t.Fatalf("ActivityEntry no longer has a %s field; "+
			"pactEntryNoDetails exists to drop it — re-derive the projection", dropped)
	}
	delete(fullFields, dropped)

	for name, want := range fullFields {
		got, ok := narrowFields[name]
		if !ok {
			t.Errorf("ActivityEntry.%s is missing from pactEntryNoDetails. "+
				"If matchesFilter ever reads it, the sources scan will match the "+
				"WRONG rows with no error. Add it to the projection (and to "+
				"pactEntryNoDetails.entry), or extend `dropped` here if it is "+
				"genuinely as expensive and as unused as Details.", name)
			continue
		}
		if got.typ != want.typ {
			t.Errorf("field %s: ActivityEntry has %s, pactEntryNoDetails has %s", name, want.typ, got.typ)
		}
		if got.tag != want.tag {
			t.Errorf("field %s: json tag %q on ActivityEntry, %q on pactEntryNoDetails "+
				"— a differing tag means the projection silently decodes nothing", name, want.tag, got.tag)
		}
		delete(narrowFields, name)
	}
	for name := range narrowFields {
		t.Errorf("pactEntryNoDetails.%s has no counterpart in ActivityEntry", name)
	}
}

// TestPactEntryNoDetails_EntryRoundTrip proves the projection carries every
// mirrored field through a real decode, so entry() cannot silently drop one.
//
// The reflection test above catches a field missing from the STRUCT; this
// catches a field present in the struct but forgotten in entry(), which is the
// same silent-wrong-filter failure by a different route.
func TestPactEntryNoDetails_EntryRoundTrip(t *testing.T) {
	pruned := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	want := ActivityEntry{
		ID:          99,
		Timestamp:   time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC),
		Tier:        "change",
		Type:        "itunes",
		Level:       "warn",
		Source:      "itunes-apply",
		OperationID: "op-123",
		BookID:      "bk-456",
		Summary:     "a summary",
		Details:     map[string]any{"big": "blob"},
		Tags:        []string{"a", "b"},
		PrunedAt:    &pruned,
	}
	blob, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var p pactEntryNoDetails
	if err := json.Unmarshal(blob, &p); err != nil {
		t.Fatalf("narrow unmarshal: %v", err)
	}
	got := p.entry()

	// Details is the one field deliberately not carried.
	if got.Details != nil {
		t.Errorf("Details = %v, want nil (the projection must not carry it)", got.Details)
	}
	wantNoDetails := want
	wantNoDetails.Details = nil
	if !reflect.DeepEqual(got, wantNoDetails) {
		t.Errorf("entry() lost or altered a field.\n got: %+v\nwant: %+v", got, wantNoDetails)
	}
}

// TestPactBucketTime_TruncatesDownAndIsStableWithinABucket pins the property
// the cache depends on: two instants in the same bucket produce the same key,
// and the bucket boundary is never in the future of the input.
func TestPactBucketTime_TruncatesDownAndIsStableWithinABucket(t *testing.T) {
	base := time.Date(2026, 9, 7, 20, 3, 0, 0, time.UTC)
	for _, off := range []time.Duration{0, time.Second, 61 * time.Second, activitySourcesCacheBucket - time.Nanosecond} {
		in := base.Add(off)
		got := pactBucketTime(in)
		if got.After(in) {
			t.Errorf("pactBucketTime(%s) = %s, which is AFTER the input; "+
				"the bucket must round DOWN so a cached answer covers at least the requested window", in, got)
		}
		if in.Sub(got) >= activitySourcesCacheBucket {
			t.Errorf("pactBucketTime(%s) = %s, %s earlier — more than one bucket of slack", in, got, in.Sub(got))
		}
	}

	// The production symptom: `since` rolls by a minute on every poll. Those
	// polls must share a cache key, or the memo can never be hit.
	a := time.Date(2026, 9, 7, 20, 3, 0, 0, time.UTC)
	b := time.Date(2026, 9, 7, 20, 4, 0, 0, time.UTC)
	fa := ActivityFilter{Since: &a}
	fb := ActivityFilter{Since: &b}
	if pactSourcesCacheKey(fa) != pactSourcesCacheKey(fb) {
		t.Errorf("a minute-rolling `since` still produces different cache keys "+
			"(%q vs %q) — the sources memo remains unreachable for the UI's poll",
			pactSourcesCacheKey(fa), pactSourcesCacheKey(fb))
	}

	// ...but bounds a full bucket apart must NOT collide.
	c := b.Add(activitySourcesCacheBucket)
	fc := ActivityFilter{Since: &c}
	if pactSourcesCacheKey(fb) == pactSourcesCacheKey(fc) {
		t.Errorf("bounds %s apart share a cache key; the bucket is not bounding anything", activitySourcesCacheBucket)
	}
}

// TestPactSourcesCacheKey_StillSeparatesEveryOtherField guards against the
// bucketing change accidentally widening what a cached answer is reused for.
func TestPactSourcesCacheKey_StillSeparatesEveryOtherField(t *testing.T) {
	base := ActivityFilter{}
	mutations := map[string]ActivityFilter{
		"Tier":           {Tier: "change"},
		"Type":           {Type: "itunes"},
		"Level":          {Level: "warn"},
		"Source":         {Source: "scanner"},
		"OperationID":    {OperationID: "op-1"},
		"BookID":         {BookID: "bk-1"},
		"Search":         {Search: "needle"},
		"Tags":           {Tags: []string{"t"}},
		"ExcludeSources": {ExcludeSources: []string{"s"}},
		"ExcludeTiers":   {ExcludeTiers: []string{"debug"}},
		"ExcludeTags":    {ExcludeTags: []string{"no-op"}},
	}
	baseKey := pactSourcesCacheKey(base)
	for name, f := range mutations {
		if pactSourcesCacheKey(f) == baseKey {
			t.Errorf("changing %s did not change the sources cache key — "+
				"one filter's counts would be served for another", name)
		}
	}
}

// buildActivityEntryJSON produces a marshalled ActivityEntry whose Details map
// holds nOps ITL-shaped operation records, mimicking the production
// ApplyITLOperations dumps that reach ~9.6 MB.
func buildActivityEntryJSON(nOps int) []byte {
	ops := make([]any, 0, nOps)
	for i := range nOps {
		ops = append(ops, map[string]any{
			"persistent_id": fmt.Sprintf("%016X", i),
			"track_id":      i,
			"name":          fmt.Sprintf("Chapter %d of a fairly long audiobook title", i),
			"artist":        "Some Author Name",
			"album":         "Some Long Album Title Goes Here",
			"location":      fmt.Sprintf("file:///Volumes/media/books/itunes/Author/Book/track%05d.m4b", i),
			"action":        "update",
			"changed":       []any{"name", "album", "artist"},
			"before":        map[string]any{"name": "old name", "album": "old album"},
			"after":         map[string]any{"name": "new name", "album": "new album"},
		})
	}
	e := ActivityEntry{
		ID:        12345,
		Timestamp: time.Now().UTC(),
		Tier:      "change",
		Type:      "itunes",
		Level:     "info",
		Source:    "itunes-apply",
		Summary:   "Applied ITL operations",
		Details:   map[string]any{"operations": ops, "count": nOps},
		Tags:      []string{"itunes", "apply"},
	}
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	return b
}

// BenchmarkDecodeSource is the measurement quoted in pactEntryNoDetails' doc
// comment. Re-run it before changing the projection away.
//
//	go test ./internal/database/ -run XXX -bench BenchmarkDecodeSource -benchmem
func BenchmarkDecodeSource(b *testing.B) {
	for _, n := range []int{0, 10, 20000} {
		blob := buildActivityEntryJSON(n)
		b.Run(fmt.Sprintf("ops=%d/bytes=%d/full", n, len(blob)), func(b *testing.B) {
			b.SetBytes(int64(len(blob)))
			for b.Loop() {
				var e ActivityEntry
				if err := json.Unmarshal(blob, &e); err != nil {
					b.Fatal(err)
				}
				if e.Source == "" {
					b.Fatal("no source")
				}
			}
		})
		b.Run(fmt.Sprintf("ops=%d/bytes=%d/narrow", n, len(blob)), func(b *testing.B) {
			b.SetBytes(int64(len(blob)))
			for b.Loop() {
				var p pactEntryNoDetails
				if err := json.Unmarshal(blob, &p); err != nil {
					b.Fatal(err)
				}
				if p.Source == "" {
					b.Fatal("no source")
				}
			}
		})
	}
}
