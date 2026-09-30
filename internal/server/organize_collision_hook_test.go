// file: internal/server/organize_collision_hook_test.go
// version: 1.1.0
// guid: b1c65824-b632-4123-aef4-5ced702b8855
// last-edited: 2026-09-30

package server

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The organize collision hook writes dedup candidates directly, bypassing the
// engine's version_group_same suppressor. On 2026-09-30 it filed a pending
// exact candidate pairing a protected book with its own library copy. It must
// skip two versions of one book and still file a real duplicate.
func TestOrganizeCollisionHook_SkipsVersionsOfOneBook(t *testing.T) {
	group := "vg-1"
	other := "vg-2"
	cases := []struct {
		name          string
		occupantGroup *string
		currentErr    error
		wantCandidate bool
	}{
		{"own library copy", &group, nil, false},
		{"unrelated duplicate", &other, nil, true},
		{"ungrouped duplicate", nil, nil, true},
		// The current book cannot be read, so the group check cannot run:
		// file the candidate (a human can dismiss it) rather than lose a
		// real duplicate.
		{"current book unreadable", &group, errors.New("pebble: closed"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			occupantPath := "/library/A/T/T.mp3"
			store := &database.MockStore{
				GetBookByIDFunc: func(id string) (*database.Book, error) {
					if tc.currentErr != nil {
						return nil, tc.currentErr
					}
					return &database.Book{ID: id, VersionGroupID: &group}, nil
				},
				GetBookByFilePathFunc: func(path string) (*database.Book, error) {
					return &database.Book{ID: "copy", FilePath: path, VersionGroupID: tc.occupantGroup}, nil
				},
			}
			emb := newTriageTestEmbeddingStore(t)
			srv := &Server{store: store, embeddingStore: emb}

			(&serverOrganizeHooks{server: srv}).OnCollision("orig", occupantPath)
			srv.bgWG.Wait()

			got, _, err := emb.ListCandidates(database.CandidateFilter{EntityType: "book"})
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantCandidate && len(got) != 1 {
				t.Errorf("filed %d candidate(s); want 1 for a real duplicate", len(got))
			}
			if !tc.wantCandidate && len(got) != 0 {
				t.Errorf("filed %d candidate(s) pairing a book with its own version; want 0", len(got))
			}
		})
	}
}
