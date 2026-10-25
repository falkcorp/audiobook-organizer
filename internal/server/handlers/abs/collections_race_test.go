// file: internal/server/handlers/abs/collections_race_test.go
// version: 1.1.0
// guid: b2a8e4f6-7c19-4d53-a0e8-3f5d1c9b6a27
// last-edited: 2026-10-25

package abs_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
)

// raceRounds is how many times each interleaving is attempted. One round proves
// little (the goroutines may simply run in sequence); fifty against the real
// store lock make a lost update overwhelmingly likely to surface.
const raceRounds = 50

// TestCollectionMembership_ABSAndNativeWritesDoNotLoseEachOther runs an ABS
// membership write and a native add-books call against ONE collection at the
// same time, against a real PebbleStore (not the fake), so the store's lock is
// what is under test. Every round both calls answer 200, both effects are in
// the final membership, and Version rose by exactly 2: one bump per effective
// change, no lost update, no spurious 409.
//
// Run with -race.
func TestCollectionMembership_ABSAndNativeWritesDoNotLoseEachOther(t *testing.T) {
	gin.SetMode(gin.TestMode)
	seed := seedOracleLibrary(t)
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db.pebble"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	h := newHarness(t, "jwt", nil, withLibrary(seed), withUserData(fixtureUserData()), withCollections(store))
	h.seedUser(t, "u1", "curator", "", "pw-pw-pw-pw")
	abscolGrantManage(t, h, "u1")
	tok := str(t, userObj(t, h.login(t, "curator", "pw-pw-pw-pw")), "accessToken")

	// The sync id is minted up front, on this goroutine.
	singleSync := absplSyncIDFor(t, seed, seed.singleID)

	native := handlers.NewCollectionHandler(store, store, &viewerEvalStore{}, nil)
	nr := gin.New()
	g := nr.Group("/api/v1")
	g.POST("/collections/:id/books", native.AddBooksToCollection)

	// call performs one request without touching *testing.T, so it is safe from
	// the racing goroutines.
	call := func(r *gin.Engine, method, path string, body any, bearerToken string) int {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+bearerToken)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	variants := []struct {
		name string
		// seedBooks is the membership each round starts with.
		seedBooks []string
		// abs is the ABS-side write racing the native add of seed.multiID.
		abs func(colID string) int
		// want is the final membership as a set.
		want []string
	}{
		{
			name: "ABS single add vs native add",
			abs: func(id string) int {
				return call(h.router, http.MethodPost, "/api/collections/"+id+"/book",
					map[string]any{"id": singleSync}, tok)
			},
			want: []string{seed.singleID, seed.multiID},
		},
		{
			name: "ABS batch add vs native add",
			abs: func(id string) int {
				return call(h.router, http.MethodPost, "/api/collections/"+id+"/batch/add",
					map[string]any{"books": []string{singleSync}}, tok)
			},
			want: []string{seed.singleID, seed.multiID},
		},
		{
			name:      "ABS remove vs native add",
			seedBooks: []string{seed.singleID},
			abs: func(id string) int {
				return call(h.router, http.MethodDelete, "/api/collections/"+id+"/book/"+singleSync, nil, tok)
			},
			// The ABS call removes b1 while the native call adds b2; neither
			// may undo the other.
			want: []string{seed.multiID},
		},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			for round := range raceRounds {
				col, err := store.CreateCollection(&database.Collection{
					Name: fmt.Sprintf("%s round %d", v.name, round),
					Type: database.CollectionTypeStatic, BookIDs: slices.Clone(v.seedBooks),
				})
				if err != nil {
					t.Fatalf("round %d: create: %v", round, err)
				}
				startVersion := col.Version

				var wg sync.WaitGroup
				var absCode, nativeCode int
				start := make(chan struct{})
				wg.Add(2)
				go func() {
					defer wg.Done()
					<-start
					absCode = v.abs(col.ID)
				}()
				go func() {
					defer wg.Done()
					<-start
					nativeCode = call(nr, http.MethodPost, "/api/v1/collections/"+col.ID+"/books",
						map[string]any{"book_ids": []string{seed.multiID}}, "")
				}()
				close(start)
				wg.Wait()

				if absCode != http.StatusOK || nativeCode != http.StatusOK {
					t.Fatalf("round %d: ABS = %d, native = %d, want both 200 (a 409 here is the old "+
						"read-modify-write conflict)", round, absCode, nativeCode)
				}
				got, err := store.GetCollection(col.ID)
				if err != nil || got == nil {
					t.Fatalf("round %d: reload: %v", round, err)
				}
				if len(got.BookIDs) != len(v.want) {
					t.Fatalf("round %d: BookIDs = %v, want exactly %v (a lost update)", round, got.BookIDs, v.want)
				}
				for _, w := range v.want {
					if !slices.Contains(got.BookIDs, w) {
						t.Fatalf("round %d: BookIDs = %v, missing %s (a lost update)", round, got.BookIDs, w)
					}
				}
				if got.Version != startVersion+2 {
					t.Fatalf("round %d: Version = %d, want %d (one bump per effective change)",
						round, got.Version, startVersion+2)
				}
			}
		})
	}
}
