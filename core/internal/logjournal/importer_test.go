// file: core/internal/logjournal/importer_test.go
// version: 1.0.0
// guid: c7e1a5d9-2b04-4f83-9d6a-3a8b0f4e1c27
// last-edited: 2026-11-13

package logjournal_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

func rowCount(t *testing.T, ks storage.Keyspace) int {
	t.Helper()
	n := 0
	for _, err := range ks.Scan(context.Background(), storage.ScanOptions{KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
	}
	return n
}

func imp(src string, d time.Duration, msg string) log.ImportedRecord {
	return log.ImportedRecord{Record: rec(d, msg), SourceID: src}
}

// The importer's key is a function of (Time, SourceID): the same record twice
// is one row, two source IDs of one time are two, and a bad record rejects the
// whole call before anything is written.
func TestImporter_IdempotentKeyAndRejections(t *testing.T) {
	ctx := context.Background()
	s, ks := openStore(t)
	im := logjournal.NewImporter(s)

	batch := []log.ImportedRecord{imp("syslog:a", 0, "alfred"), imp("syslog:b", 0, "barbara"), imp("syslog:c", time.Second, "cassandra")}
	for range 3 {
		if err := im.Import(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	if n := rowCount(t, ks); n != 3 {
		t.Fatalf("3 distinct sources imported 3 times left %d rows, want 3", n)
	}

	// A bad record anywhere in the call: nothing written.
	for name, bad := range map[string]log.ImportedRecord{
		"no source id": imp("", 0, "nameless"),
		"zero time":    {Record: log.Record{Message: "timeless"}, SourceID: "syslog:z"},
		"pre-epoch":    {Record: log.Record{Time: time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC), Message: "old"}, SourceID: "syslog:y"},
	} {
		err := im.Import(ctx, []log.ImportedRecord{imp("syslog:good", 5*time.Second, "good"), bad})
		if err == nil {
			t.Errorf("%s: Import succeeded", name)
		}
		if n := rowCount(t, ks); n != 3 {
			t.Errorf("%s: %d rows after a rejected call, want 3", name, n)
		}
	}
	if err := im.Import(ctx, []log.ImportedRecord{imp("", 0, "x")}); !errors.Is(err, logjournal.ErrNoSourceID) {
		t.Errorf("no source id: err = %v, want ErrNoSourceID", err)
	}
	if err := im.Import(ctx, []log.ImportedRecord{{Message: "timeless", SourceID: "s"}}); !errors.Is(err, logjournal.ErrInvalidTime) {
		t.Errorf("zero time: err = %v, want ErrInvalidTime", err)
	}

	// A re-import of a record whose content changed overwrites it in place.
	if err := im.Import(ctx, []log.ImportedRecord{imp("syslog:a", 0, "alfred, edited")}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Query(ctx, all())
	wantMessages(t, "after an edited re-import", p.Records, "cassandra", "alfred, edited", "barbara")
}

// crockford is ULID's alphabet; a key's first ten characters are its
// millisecond timestamp in it.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func ulidMillis(t *testing.T, ulid string) uint64 {
	t.Helper()
	if len(ulid) != 26 {
		t.Fatalf("%q is not a 26-character ULID", ulid)
	}
	var ms uint64
	for _, c := range ulid[:10] {
		i := strings.IndexRune(crockford, c)
		if i < 0 {
			t.Fatalf("%q is not Crockford base32", ulid)
		}
		ms = ms<<5 | uint64(i)
	}
	return ms
}

// An imported record's stored key sorts at the record's own millisecond: Query
// orders by key and Prune cuts by key, so a key a millisecond off would place the
// record wrongly beside its native neighbours.
func TestImporter_KeyCarriesTheRecordsMillisecond(t *testing.T) {
	ctx := context.Background()
	s, ks := openStore(t)
	im := logjournal.NewImporter(s)
	recs := []log.ImportedRecord{imp("syslog:a", 0, "alfred"), imp("syslog:b", 1234*time.Millisecond, "barbara")}
	if err := im.Import(ctx, recs); err != nil {
		t.Fatal(err)
	}
	got := map[uint64]bool{}
	for kv, err := range ks.Scan(ctx, storage.ScanOptions{KeysOnly: true}) {
		if err != nil {
			t.Fatal(err)
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) != 2 {
			t.Fatalf("key %q: %v", kv.Key, err)
		}
		got[ulidMillis(t, string(segs[1]))] = true
	}
	for _, r := range recs {
		if ms := uint64(r.Time.UnixMilli()); !got[ms] {
			t.Errorf("no key at the record's millisecond %d (keys at %v)", ms, got)
		}
	}
	if len(got) != len(recs) {
		t.Errorf("%d distinct key milliseconds, want %d", len(got), len(recs))
	}
}
