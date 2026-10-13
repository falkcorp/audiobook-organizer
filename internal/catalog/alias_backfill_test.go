// file: internal/catalog/alias_backfill_test.go
// version: 1.0.0
// guid: d4b71e90-2f5c-4a68-9e13-7c8a0b5f2d46
// last-edited: 2026-10-12

package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// asinScanner is an authority.Scanner over a fixed ref_asin:person: keyspace.
type asinScanner struct{ rows map[string]authority.ASINRef }

func (s asinScanner) GetRaw(string) ([]byte, error) { return nil, nil }
func (s asinScanner) ScanPrefixPage(prefix, after string, _ int) ([]database.KVPair, string, error) {
	if after != "" {
		return nil, "", nil
	}
	var out []database.KVPair
	for asin, ref := range s.rows {
		if !strings.HasPrefix(authority.ASINPrefix+"person:"+asin, prefix) {
			continue
		}
		b, _ := json.Marshal(ref)
		out = append(out, database.KVPair{Key: authority.ASINPrefix + "person:" + asin, Value: b})
	}
	return out, "", nil
}

// Entries stored before the alias write-through existed get their aliases
// from one backfill pass, and a second pass writes nothing. A contributor ASIN
// the authority lists saw under two spellings aliases the unharvested one to
// the harvested one; an ASIN whose spellings both have blocks, or neither,
// files nothing.
func TestBackfillAuthorAliases(t *testing.T) {
	st, _ := openCatalog(t)
	// Direct upserts: the entries exist, the aliases do not.
	seedProducts(t, st, "janeqauthor", block("Jane Q. Author", "AUTHJQA", 3, 1101)...)
	seedProducts(t, st, "ann", block("Ann Both", "AUTHANN", 1, 1201)...)
	seedProducts(t, st, "annb", block("Anne Both", "AUTHANN", 1, 1301)...)
	_, ok, err := st.ResolveAuthorAlias("janeauthor")
	require.NoError(t, err)
	require.False(t, ok, "fixture: nothing is filed yet")

	kv := asinScanner{rows: map[string]authority.ASINRef{
		"AUTHJQA": {ASIN: "AUTHJQA", Folds: []string{"janeqauthor", "janequinnauthor"}}, // one block: alias it
		"AUTHANN": {ASIN: "AUTHANN", Folds: []string{"annboth", "anneboth"}},            // two blocks: ambiguous, nothing
		"AUTHNON": {ASIN: "AUTHNON", Folds: []string{"nobodyone", "nobodytwo"}},         // no block: nothing
		"AUTHONE": {ASIN: "AUTHONE", Folds: []string{"janeqauthor"}},                    // one spelling: nothing
	}}
	res, err := BackfillAuthorAliases(context.Background(), st, kv)
	require.NoError(t, err)
	assert.Equal(t, 5, res.Entries)
	assert.Equal(t, 3, res.Names)
	assert.Equal(t, 1, res.AuthorityPairs)
	assert.Positive(t, res.Written)

	for alias, want := range map[string]string{
		"janeauthor":      "janeqauthor",
		"authorjaneq":     "janeqauthor",
		"janequinnauthor": "janeqauthor", // from the authority list
	} {
		got, ok, err := st.ResolveAuthorAlias(alias)
		require.NoError(t, err)
		assert.True(t, ok, alias)
		assert.Equal(t, want, got, alias)
	}
	q, err := MatchBook(st, BookQuestion{Titles: []string{"Title 001102"}, Credits: []string{"Jane Quinn Author"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"Title 001102"}, titlesOf(q), "the authority spelling now reaches the block")

	again, err := BackfillAuthorAliases(context.Background(), st, kv)
	require.NoError(t, err)
	assert.Zero(t, again.Written, "idempotent: everything is already filed")

	// A nil store and a nil authority are no-ops, not errors.
	none, err := BackfillAuthorAliases(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Zero(t, none.Entries)
	noAuth, err := BackfillAuthorAliases(context.Background(), st, nil)
	require.NoError(t, err)
	assert.Zero(t, noAuth.AuthorityPairs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = BackfillAuthorAliases(ctx, st, kv)
	assert.Error(t, err, "a cancelled run stops")
}
