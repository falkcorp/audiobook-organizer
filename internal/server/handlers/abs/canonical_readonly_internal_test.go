// file: internal/server/handlers/abs/canonical_readonly_internal_test.go
// version: 1.0.1
// guid: f16aca7b-73fa-4183-b604-cfce7af5fb36
// last-edited: 2026-09-25

package abs

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mintCountingIdentity fails the test if anything mints (writes) a sync id.
type mintCountingIdentity struct {
	t      *testing.T
	byBook map[string]string
	items  map[string]*database.SyncItem
	mints  int
}

func (m *mintCountingIdentity) MintOrGetSyncID(bookID string) (string, error) {
	m.mints++
	return m.byBook[bookID], nil
}
func (m *mintCountingIdentity) ResolveSyncItem(syncID string) (*database.SyncItem, error) {
	it := m.items[syncID]
	for it != nil && it.RedirectTo != "" {
		it = m.items[it.RedirectTo]
	}
	return it, nil
}
func (m *mintCountingIdentity) GetSyncIDForBook(bookID string) (string, bool, error) {
	id, ok := m.byBook[bookID]
	return id, ok, nil
}
func (m *mintCountingIdentity) MintOrGetSyncFileID(string, string) (string, error) { return "", nil }
func (m *mintCountingIdentity) MintOrGetSyncFileIDs(string, []string) (map[string]string, error) {
	return nil, nil
}
func (m *mintCountingIdentity) GetSyncFileID(string, string) (string, bool, error) {
	return "", false, nil
}
func (m *mintCountingIdentity) ListSyncAliases(string) ([]string, error) { return nil, nil }

func (m *mintCountingIdentity) ListSyncFilesForBook(string) ([]database.SyncFile, error) {
	return nil, nil
}

// Review LOW #5: canonicalBookID runs on every render and every retry, so it
// must be a pure read — it used to call MintOrGetSyncID (a write path).
func TestCanonicalBookID_IsReadOnly(t *testing.T) {
	id := &mintCountingIdentity{t: t,
		byBook: map[string]string{"loser": "s-loser", "winner": "s-winner"},
		items: map[string]*database.SyncItem{
			"s-loser":  {SyncID: "s-loser", RedirectTo: "s-winner"},
			"s-winner": {SyncID: "s-winner", CurrentBookID: "winner"},
		}}
	h := &Handler{identity: id}
	if got := h.canonicalBookID("loser"); got != "winner" {
		t.Fatalf("canonicalBookID(loser) = %q, want winner", got)
	}
	if got := h.canonicalBookID("never-minted"); got != "never-minted" {
		t.Fatalf("canonicalBookID(never-minted) = %q", got)
	}
	_ = h.canonicalMembers([]string{"loser", "winner", "never-minted"})
	_ = h.withoutMembers([]string{"loser", "never-minted"}, []string{"winner"})
	if id.mints != 0 {
		t.Fatalf("canonical lookups minted %d sync ids; they must be read-only", id.mints)
	}
}
