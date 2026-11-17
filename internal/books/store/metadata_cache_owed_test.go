// file: internal/books/store/metadata_cache_owed_test.go
// version: 1.0.0
// guid: 0b6c3f82-9e47-4d15-b8a3-5f2d7e91c604
// last-edited: 2026-11-16

package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The backoff of a chain source a cache row owes an answer (OwedSources): when
// it is due, when it is out of asks, and the flag the summary index carries.

func TestOwedSource_ScheduleAndCap(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 5, OwedMaxAsks, "the cap is stated as 5 asks in the PR and the changelog")
	require.Equal(t, [...]time.Duration{6 * time.Hour, 24 * time.Hour, 72 * time.Hour, 168 * time.Hour}, OwedBackoff)

	for attempts, wait := range map[int]time.Duration{
		0: 6 * time.Hour, // a first failure that was only a hold: the first wait
		1: 6 * time.Hour, 2: 24 * time.Hour, 3: 72 * time.Hour, 4: 168 * time.Hour,
	} {
		o := OwedSource{Since: t0, LastAttempt: t0, Attempts: attempts}
		at, ok := o.NextAttempt()
		require.True(t, ok, "attempts=%d", attempts)
		require.Equal(t, t0.Add(wait), at, "attempts=%d", attempts)
		require.False(t, o.Due(t0.Add(wait-time.Nanosecond)), "attempts=%d due early", attempts)
		require.True(t, o.Due(t0.Add(wait)), "attempts=%d not due on time", attempts)
		require.True(t, o.Due(t0.Add(wait+time.Hour)), "attempts=%d", attempts)
		require.False(t, o.Exhausted(), "attempts=%d", attempts)
	}

	// The fifth failed ask uses the last one: never due again.
	for _, attempts := range []int{OwedMaxAsks, OwedMaxAsks + 3} {
		o := OwedSource{Since: t0, LastAttempt: t0, Attempts: attempts}
		require.True(t, o.Exhausted(), "attempts=%d", attempts)
		_, ok := o.NextAttempt()
		require.False(t, ok)
		require.False(t, o.Due(t0.Add(10_000*time.Hour)), "an exhausted source was due")
	}
}

// Total span of the schedule from the first failure to the last re-ask: what the
// PR states ("about 11.5 days").
func TestOwedSource_ScheduleSpan(t *testing.T) {
	var span time.Duration
	for _, d := range OwedBackoff {
		span += d
	}
	require.Equal(t, 11*24*time.Hour+6*time.Hour, span)
}

func TestOwedDue_OnlyActiveDueSourcesSorted(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	c := &MetadataCandidateCache{OwedSources: map[string]OwedSource{
		"Zeta":      {LastAttempt: t0, Attempts: 1},
		"Alpha":     {LastAttempt: t0, Attempts: 1},
		"NotDue":    {LastAttempt: t0.Add(5 * time.Hour), Attempts: 1},
		"Exhausted": {LastAttempt: t0.Add(-1000 * time.Hour), Attempts: OwedMaxAsks},
		"Inactive":  {LastAttempt: t0.Add(-1000 * time.Hour), Attempts: 1},
	}}
	now := t0.Add(6 * time.Hour)
	require.Equal(t, []string{"Alpha", "Zeta"}, c.OwedDue(now, []string{"Zeta", "Alpha", "NotDue", "Exhausted", "Alpha"}))
	require.Nil(t, c.OwedDue(now, nil))
	require.Nil(t, (&MetadataCandidateCache{}).OwedDue(now, []string{"Alpha"}), "a row written before the field owes nothing")
	var nilRow *MetadataCandidateCache
	require.Nil(t, nilRow.OwedDue(now, []string{"Alpha"}))

	require.True(t, c.OwedPending())
	require.False(t, (&MetadataCandidateCache{OwedSources: map[string]OwedSource{"X": {Attempts: OwedMaxAsks}}}).OwedPending())
	require.False(t, (&MetadataCandidateCache{}).OwedPending())
	require.False(t, nilRow.OwedPending())
}

// The field is additive: a row without it decodes to nil and writes no key.
func TestOwedSources_AdditiveJSON(t *testing.T) {
	var old MetadataCandidateCache
	require.NoError(t, json.Unmarshal([]byte(`{"book_id":"b1","source_hash":"h"}`), &old))
	require.Nil(t, old.OwedSources)
	blob, err := json.Marshal(MetadataCandidateCache{BookID: "b1"})
	require.NoError(t, err)
	require.NotContains(t, string(blob), "owed_sources")
}

// The summary's Owed flag survives the first full scan and the incremental
// refresh of a changed row, in both directions, and drops once every owed source
// is out of asks (a row nobody can re-ask is not "owed" to the selection).
func TestListMetadataCacheKeys_CarriesOwed(t *testing.T) {
	s := newKeepCacheStore(t)
	cand := []json.RawMessage{json.RawMessage(`{"title":"Title 000124"}`)}
	put := func(id string, owed map[string]OwedSource) {
		t.Helper()
		require.NoError(t, s.PutMetadataCache(&MetadataCandidateCache{BookID: id, FetchedAt: time.Now(),
			SourceHash: "h", Candidates: cand, OwedSources: owed}))
	}
	pending := map[string]OwedSource{"Audnexus": {LastAttempt: time.Now(), Attempts: 2}}
	put("owed", pending)
	put("plain", nil)
	put("spent", map[string]OwedSource{"Audnexus": {LastAttempt: time.Now(), Attempts: OwedMaxAsks}})

	owedOf := func(id string) bool { return staleSummaryOf(t, s, id).Owed }
	require.True(t, owedOf("owed"))
	require.False(t, owedOf("plain"))
	require.False(t, owedOf("spent"))

	put("owed", nil)
	put("plain", pending)
	put("later", pending)
	require.False(t, owedOf("owed"))
	require.True(t, owedOf("plain"))
	require.True(t, owedOf("later"))
}
