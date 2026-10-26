// file: internal/playlist/evaluator_tag_terms_test.go
// version: 1.0.1
// guid: 2f9c6b13-84d7-4a50-b1e8-d3a07c5f9e46
// last-edited: 2026-10-26

package playlist

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// tagFixture is collectionFixture with tags on its four books, in the index and in
// the store. alice has finished b1 and b2 and is part-way through b3; bob has
// finished b2 and b4. The tags are chosen so a match by word differs from the
// exact match: "dark-knight" holds the word another tag is, "the-batman" reduces
// to "batman" once the stop word is gone.
//
//	b1  noir, dark-knight
//	b2  noir, knight
//	b3  the-batman
//	b4  batman
func tagFixture(t *testing.T) (*database.PebbleStore, *search.BleveIndex) {
	t.Helper()
	store, idx, _ := collectionFixture(t)
	tags := map[string][]string{
		"b1": {"noir", "dark-knight"},
		"b2": {"noir", "knight"},
		"b3": {"the-batman"},
		"b4": {"batman"},
	}
	for id, ts := range tags {
		for _, tag := range ts {
			require.NoError(t, store.AddBookTag(id, tag))
		}
		require.NoError(t, idx.IndexBook(search.BookDocument{
			BookID: id, Title: "Synthetic " + id, Author: "Test Author", Tags: ts,
		}))
	}
	return store, idx
}

// A stored dynamic collection or smart playlist whose query has a tag: term is
// evaluated EXACTLY: the books that carry the whole tag, never the books whose tag
// shares a word with it. Before tag: was an exact filter the index matched it by
// word (tag:"dark-knight" also found the "knight" book; tag:batman found the
// "the-batman" one), and before it was spelled at all the collection was empty.
func TestEvaluate_ATagTermIsAnExactMembershipFilter(t *testing.T) {
	store, idx := tagFixture(t)
	for _, tc := range []struct {
		name, query string
		alice, bob  []string
	}{
		{"one tag", `tag:"dark-knight"`, []string{"b1"}, []string{"b1"}},
		{"its word is another tag", `tag:knight`, []string{"b2"}, []string{"b2"}},
		{"folded like GetBooksByTag", `tag:"Dark-Knight"`, []string{"b1"}, []string{"b1"}},
		{"stop word in the tag", `tag:"the-batman"`, []string{"b3"}, []string{"b3"}},
		{"the tag a stop word reduces to", `tag:batman`, []string{"b4"}, []string{"b4"}},
		{"a word of a tag is not a tag", `tag:dark`, []string{}, []string{}},
		{"two tags are both required", `tag:noir tag:knight`, []string{"b2"}, []string{"b2"}},
		{"no book has both", `tag:knight tag:batman`, []string{}, []string{}},
		{"with a per-user term", `read_status:finished tag:noir`, []string{"b1", "b2"}, []string{"b2"}},
		{"explicit AND, grouped", `(read_status:finished AND tag:noir)`, []string{"b1", "b2"}, []string{"b2"}},
		{"per-user term narrows the tag", `read_status:finished tag:batman`, []string{}, []string{"b4"}},
		{"in progress", `read_status:in_progress tag:"the-batman"`, []string{"b3"}, []string{}},
		{"with an ordinary field", `tag:noir title:"Synthetic b2"`, []string{"b2"}, []string{"b2"}},
		{"nobody has it", `tag:camelot`, []string{}, []string{}},
		// a stop word or bare punctuation next to the tag is noise, not a search term
		{"stop word next to the tag", `the tag:noir`, []string{"b1", "b2"}, []string{"b1", "b2"}},
		{"dangling minus next to the tag", `- tag:noir`, []string{"b1", "b2"}, []string{"b1", "b2"}},
		{"dangling OR in a group", `(tag:noir OR)`, []string{"b1", "b2"}, []string{"b1", "b2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotAlice := evalAs(t, store, idx, tc.query, "alice")
			gotBob := evalAs(t, store, idx, tc.query, "bob")
			assert.True(t, slices.Equal(gotAlice, tc.alice), "alice: got %v, want %v", gotAlice, tc.alice)
			assert.True(t, slices.Equal(gotBob, tc.bob), "bob: got %v, want %v", gotBob, tc.bob)
		})
	}

	// No viewer still matches nothing for a per-user rule, and a tag-only rule has
	// no per-user part, so it answers for the system too.
	assert.Empty(t, evalAs(t, store, idx, `read_status:finished tag:noir`, ""))
	assert.Equal(t, []string{"b1", "b2"}, evalAs(t, store, idx, `tag:noir`, ""))
}

// A tag: term that cannot be applied exactly makes the stored definition
// unavailable, with the sentence the person got when saving, never a membership.
func TestEvaluate_AGroupedOrNegatedTagTermIsRefused(t *testing.T) {
	store, idx := tagFixture(t)
	for _, q := range []string{
		"tag:noir OR tag:knight",
		"title:Synthetic (tag:noir OR author:Test)",
		"-tag:noir",
		"NOT (tag:noir title:Synthetic)",
		"tag:(noir|batman)",
		"tag:noir*",
		"tag:no*ir",
		`tag:"noir"^2`,
		"tag:noir~1",
	} {
		t.Run(q, func(t *testing.T) {
			for _, user := range []string{"alice", ""} {
				ids, err := EvaluateSmartPlaylist(context.Background(), store, idx, q, "", 0, user)
				require.Error(t, err, "user %q", user)
				assert.True(t, search.IsTagTermError(err), "user %q: %v", user, err)
				assert.True(t, IsDefinitionError(err), "a refused definition, not a server failure")
				assert.Nil(t, ids)
			}
			// Before the index state, like the per-user refusals.
			_, err := EvaluateSmartPlaylist(context.Background(), store, nil, q, "", 0, "alice")
			assert.True(t, search.IsTagTermError(err), "%v", err)
		})
	}
}

// A store that cannot list a tag's books must fail the evaluation: dropping the
// term would widen the membership to every book the rest of the query matches.
type noTagStore struct{ inner *database.PebbleStore }

func (n noTagStore) GetBookByID(id string) (*database.Book, error) { return n.inner.GetBookByID(id) }
func (n noTagStore) GetUserBookState(u, b string) (*database.UserBookState, error) {
	return n.inner.GetUserBookState(u, b)
}

func TestEvaluate_ATagTermFailsClosedOnAStoreThatCannotListATag(t *testing.T) {
	store, idx := tagFixture(t)
	_, err := EvaluateSmartPlaylist(context.Background(), noTagStore{store}, idx, `tag:noir`, "", 0, "alice")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot list a tag's books")
	// a query with no tag: term does not need the capability
	got, err := EvaluateSmartPlaylist(context.Background(), noTagStore{store}, idx, `read_status:finished`, "", 0, "alice")
	require.NoError(t, err)
	assert.Equal(t, []string{"b1", "b2"}, got)
}
