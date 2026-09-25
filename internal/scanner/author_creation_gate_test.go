// file: internal/scanner/author_creation_gate_test.go
// version: 1.0.0
// guid: 8c4f1e27-5b9a-4d3c-b6e2-0a7d9f3c1b58
// last-edited: 2026-09-25

package scanner

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func useMockStoreForAuthorGate(t *testing.T) *dbmocks.MockStore {
	t.Helper()
	store := dbmocks.NewMockStore(t)
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	SetStore(store)
	t.Cleanup(func() { database.SetGlobalStore(origStore); SetStore(nil) })
	return store
}

// TestResolveAuthorID_JunkNameCreatesNoAuthor: the scanner resolves book.Author
// (from tags, the folder parse or the AI parse) through resolveAuthorID. A junk
// name must neither be created NOR looked up -- an existing junk row would
// otherwise collect every new book -- and it must not fail the save.
//
// The mockery store fails the test on ANY unexpected call, so no EXPECT() here
// is the assertion that GetAuthorByName and CreateAuthor are never reached.
func TestResolveAuthorID_JunkNameCreatesNoAuthor(t *testing.T) {
	for _, name := range []string{
		"14 BBY", "13 short stories", "(c) 2001 Stephen Hawking", "- Epigraph",
		"&#169", "read by narrator", "Read by Robin Sachs", "Book 1 (Unabridged)",
		"02-25", "2", "Lords of the Sith_418m_07s_99h__477m_51s_99h", "Unknown Author",
	} {
		t.Run(name, func(t *testing.T) {
			store := useMockStoreForAuthorGate(t)
			id, err := resolveAuthorID(name)
			require.NoError(t, err, "a junk author name must not fail the book save")
			assert.Nil(t, id)
			store.AssertNotCalled(t, "GetAuthorByName", mock.Anything)
			store.AssertNotCalled(t, "CreateAuthor", mock.Anything)
		})
	}
}

// TestResolveAuthorID_StoreGateRefusalIsNoAuthor: if the store's own gate
// refuses a name the scanner let through, the book is saved authorless rather
// than the whole save failing.
func TestResolveAuthorID_StoreGateRefusalIsNoAuthor(t *testing.T) {
	store := useMockStoreForAuthorGate(t)
	store.EXPECT().GetAuthorByName("Jane Doe").Return(nil, nil)
	store.EXPECT().CreateAuthor("Jane Doe").Return(nil, &database.ImplausibleAuthorNameError{Name: "Jane Doe", Reason: "test"})

	id, err := resolveAuthorID("Jane Doe")
	require.NoError(t, err)
	assert.Nil(t, id)
}

// TestResolveAuthorID_RealPenNameStillCreated guards the other direction.
func TestResolveAuthorID_RealPenNameStillCreated(t *testing.T) {
	store := useMockStoreForAuthorGate(t)
	store.EXPECT().GetAuthorByName("Zogarth").Return(nil, nil)
	store.EXPECT().CreateAuthor("Zogarth").Return(&database.Author{ID: 7, Name: "Zogarth"}, nil)

	id, err := resolveAuthorID("Zogarth")
	require.NoError(t, err)
	require.NotNil(t, id)
	assert.Equal(t, 7, *id)
}
