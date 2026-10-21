// file: internal/decisions/helpers_test.go
// version: 1.0.0
// guid: b5e03c18-7a64-4d29-8f10-c94a6e27d3b8
// last-edited: 2026-10-21

package decisions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

const (
	testKind = "repairs:dedup.split-books"
	testType = "book"
)

// newKV opens a real in-memory Pebble store: ApplyRawBatch atomicity is the
// property under test, so nothing here fakes it.
func newKV(t testing.TB) *database.PebbleStore {
	t.Helper()
	p, err := database.NewPebbleStoreInMemory("db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// fixedClock steps one second per call from a fixed instant, so ids and
// timestamps are deterministic.
func fixedClock() func() time.Time {
	t := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

func newStore(t testing.TB) (*Store, *database.PebbleStore) {
	t.Helper()
	kv := newKV(t)
	return New(kv, WithClock(fixedClock()), WithEntropy(bytes.NewReader(bytes.Repeat([]byte{7}, 1<<16)))), kv
}

func humanReject(ids ...string) Input {
	return Input{
		OpKind: testKind, EntityType: testType, EntityIDs: ids,
		Decision: DecisionReject, Source: SourceHuman, Family: FamilySameWork,
		ReasonCode: "distinct_titles", ReasonText: "four different books",
		ActorUserID: "u1", ActorName: "Owner",
	}
}

func labelInput(d Decision, ids ...string) Input {
	in := humanReject(ids...)
	in.Decision, in.ReasonCode, in.ReasonText = d, "", ""
	return in
}

func mustRecord(t testing.TB, s *Store, in Input) *Record {
	t.Helper()
	rec, err := s.Record(in)
	require.NoError(t, err)
	return rec
}

func countPrefix(t testing.TB, kv *database.PebbleStore, prefix string) int {
	t.Helper()
	n, err := kv.CountPrefix(prefix)
	require.NoError(t, err)
	return int(n)
}

// forceEdge writes a pair edge the way PR8's never-merge override will: with
// FamilyAny, straight to the store. Record refuses FamilyAny on purpose.
func forceEdge(t testing.TB, kv KV, typ, a, b, family string) {
	t.Helper()
	lo, hi := orderPair(a, b)
	require.NoError(t, kv.ApplyRawBatch([]database.RawWrite{{Key: edgeKey(typ, lo, hi, family, "01FORCED0000000000000000AA")}}))
}

func subjectOf(in Input) string {
	return SubjectKey(in.OpKind, in.EntityType, in.EntityIDs, in.Scope)
}

func ids(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return out
}

func mustVetoed(t testing.TB, s *Store, typ, a, b, family string) bool {
	t.Helper()
	got, err := s.PairVetoed(typ, a, b, family)
	require.NoError(t, err)
	return got
}

func mustLoad(t testing.TB, s *Store, r IDResolver) *Vetoes {
	t.Helper()
	v, err := s.Load(context.Background(), r)
	require.NoError(t, err)
	return v
}

func snapVetoed(t testing.TB, v *Vetoes, typ, a, b, family string) bool {
	t.Helper()
	got, err := v.PairVetoed(typ, a, b, family)
	require.NoError(t, err)
	return got
}

// failingKV wraps a KV and fails the calls it is told to.
type failingKV struct {
	KV
	failApply bool
	failScan  string // fail ScanPrefixPage when the prefix starts with this
}

func (f *failingKV) ApplyRawBatch(w []database.RawWrite) error {
	if f.failApply {
		return errors.New("injected commit failure")
	}
	return f.KV.ApplyRawBatch(w)
}

func (f *failingKV) ScanPrefixPage(prefix, after string, limit int) ([]database.KVPair, string, error) {
	if f.failScan != "" && strings.HasPrefix(prefix, f.failScan) {
		return nil, "", errors.New("injected scan failure")
	}
	return f.KV.ScanPrefixPage(prefix, after, limit)
}
