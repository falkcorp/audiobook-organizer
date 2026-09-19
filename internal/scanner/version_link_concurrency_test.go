// file: internal/scanner/version_link_concurrency_test.go
// version: 1.0.0
// guid: 5c1d7a84-0f2b-49e6-9a31-7d6c4b8e2f05
// last-edited: 2026-09-19

package scanner

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// vlConcStore is the concurrent twin of vlStore: it serves the sibling lookup
// and a CreateBook-alike under its own mutex, so the only thing serialising
// the read -> elect -> write sequence is the stripe lock under test.
type vlConcStore struct {
	*database.MockStore
	mu   sync.Mutex
	rows map[string]*database.Book
	seq  int
}

func newVLConcStore(rows ...*database.Book) *vlConcStore {
	s := &vlConcStore{MockStore: &database.MockStore{}, rows: map[string]*database.Book{}}
	for _, r := range rows {
		s.rows[r.ID] = r
	}
	s.MockStore.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		cur, ok := s.rows[id]
		if !ok {
			return nil, nil
		}
		cp := *cur
		if err := fn(&cp); err != nil {
			if errors.Is(err, database.ErrSkipBookWrite) {
				return cur, nil
			}
			return nil, err
		}
		s.rows[id] = &cp
		return &cp, nil
	}
	s.MockStore.GetBooksByTitleInDirFunc = func(normalizedTitle, dirPath string) ([]database.Book, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []database.Book
		for _, r := range s.rows {
			if strings.EqualFold(r.Title, normalizedTitle) && filepath.Dir(r.FilePath) == dirPath {
				out = append(out, *r)
			}
		}
		return out, nil
	}
	return s
}

// publish stands in for CreateBook: it mints an ID and publishes the row,
// which is the moment another worker can see it.
func (s *vlConcStore) publish(b *database.Book) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	cp := *b
	cp.ID = fmt.Sprintf("n%d", s.seq)
	s.rows[cp.ID] = &cp
}

// Two new files landing in one folder at once must not each crown themselves.
// The row being decided for is invisible to the other worker until it is
// published, so the stripe lock has to span read -> elect -> publish; without
// it this fails intermittently. Run under -race.
func TestSmartVersionLink_ConcurrentArrivalsElectOnePrimary(t *testing.T) {
	dir := "/lib/Author/Foundation"
	store := newVLConcStore(&database.Book{
		ID: "b1", Title: "Foundation", FilePath: dir + "/Foundation.mp3", Format: "mp3", Duration: intPtr(36000),
	})
	prev := getStore()
	SetStore(store)
	t.Cleanup(func() { SetStore(prev) })

	arrive := func(format string) {
		row := &database.Book{
			Title: "Foundation", FilePath: dir + "/Foundation." + format, Format: format, Duration: intPtr(36000),
		}
		// Exactly the sequence scanner.go runs, lock span included.
		unlock := lockVersionLinkFor(row, dir)
		if versionLinkEligible(row) {
			siblings, err := getStore().GetBooksByTitleInDir(strings.ToLower(row.Title), dir)
			if err == nil && len(siblings) > 0 {
				applySmartVersionLink(row, siblings, dir)
			}
		}
		store.publish(row)
		unlock()
	}

	var wg sync.WaitGroup
	for _, f := range []string{"m4b", "ogg"} {
		wg.Add(1)
		go func(format string) {
			defer wg.Done()
			arrive(format)
		}(f)
	}
	wg.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	byGroup := map[string]int{}
	for _, r := range store.rows {
		if r.VersionGroupID == nil || *r.VersionGroupID == "" {
			continue
		}
		if _, seen := byGroup[*r.VersionGroupID]; !seen {
			byGroup[*r.VersionGroupID] = 0
		}
		if database.EffectiveIsPrimaryVersion(r.IsPrimaryVersion) {
			byGroup[*r.VersionGroupID]++
		}
	}
	if len(byGroup) == 0 {
		t.Fatal("no version group was formed: the fixture no longer exercises the race")
	}
	for group, primaries := range byGroup {
		if primaries != 1 {
			t.Errorf("version group %s has %d primaries, want exactly 1", group, primaries)
		}
	}
}
