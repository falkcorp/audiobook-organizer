// file: internal/database/store_member_read_test.go
// version: 1.0.0
// guid: 40c3e4b4-cc97-40db-b080-eda8f4d16cab
// last-edited: 2026-09-12

package database

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the member-read rule for set reads: a read that walks an
// index and point-reads each member may skip a member only when the getter
// reports it not-found ((nil, nil), a stale index entry). Any other error
// must fail the call. Until 2026-09-12 every one of these skipped a member on
// ANY error, so one unreadable row made the set come back short with a nil
// error. A corrupt JSON value is a real non-not-found error from each getter
// (the unmarshal fails), so no seam is needed to inject it.

const corruptRow = "{not json"

// TestGroupReads_UnreadableMemberFails covers GetBooksByVersionGroup and
// GetBooksByWorkID. Callers (dedup, version-group operations, library-copy
// lookup, the works API) act on the returned group as if it were complete.
func TestGroupReads_UnreadableMemberFails(t *testing.T) {
	cases := []struct {
		name  string
		index func(id string) string
		read  func(p *PebbleStore) ([]Book, error)
	}{
		{"GetBooksByVersionGroup",
			func(id string) string { return "book:versiongroup:vg-mr:" + id },
			func(p *PebbleStore) ([]Book, error) { return p.GetBooksByVersionGroup("vg-mr") }},
		{"GetBooksByWorkID",
			func(id string) string { return "book:work:wk-mr:" + id },
			func(p *PebbleStore) ([]Book, error) { return p.GetBooksByWorkID("wk-mr") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := setupTestPebbleStore(t)
			p.WaitForWarmup()
			vg, wk := "vg-mr", "wk-mr"
			for _, id := range []string{"mr-a", "mr-b"} {
				_, err := p.CreateBook(&Book{ID: id, Title: "Member " + id, FilePath: "/lib/mr/" + id + ".m4b",
					VersionGroupID: &vg, WorkID: &wk})
				require.NoError(t, err)
			}

			// A stale index entry (no book row) is not-found and is skipped.
			require.NoError(t, p.db.Set([]byte(tc.index("mr-stale")), nil, nil))
			got, err := tc.read(p)
			require.NoError(t, err, "a stale index entry must be skipped, not fail the group")
			require.Len(t, got, 2)

			// An unreadable member must fail the group, not shorten it.
			require.NoError(t, p.db.Set([]byte("book:mr-bad"), []byte(corruptRow), nil))
			require.NoError(t, p.db.Set([]byte(tc.index("mr-bad")), nil, nil))
			got, err = tc.read(p)
			require.Error(t, err, "an unreadable member must fail the read, not return a short group")
			require.ErrorContains(t, err, "mr-bad")
			require.Nil(t, got)
		})
	}
}

// TestSetReads_UnreadableMemberFails covers the other index-then-point-read
// lists in the store. Each also had a raw iterator whose read error was never
// checked; the scan-fault step pins that half.
func TestSetReads_UnreadableMemberFails(t *testing.T) {
	type seedFn func(t *testing.T, p *PebbleStore, id string)
	indexOnly := func(prefix string) seedFn {
		return func(t *testing.T, p *PebbleStore, id string) {
			require.NoError(t, p.db.Set([]byte(prefix+id), nil, nil))
		}
	}
	err1 := func(_ any, err error) error { return err }
	cases := []struct {
		name       string
		scanPrefix string                 // the index range the list walks
		index      seedFn                 // writes the index entry for id
		record     func(id string) string // the member's record key
		run        func(p *PebbleStore) error
	}{
		{"GetBookVersionsByBookID", "idx:bv:book:bk1:", indexOnly("idx:bv:book:bk1:"),
			func(id string) string { return "bv:" + id },
			func(p *PebbleStore) error { return err1(p.GetBookVersionsByBookID("bk1")) }},
		{"ListABSSessionsForUser", absSessionUserIdxPfx + "u1:", indexOnly(absSessionUserIdxPfx + "u1:"),
			func(id string) string { return string(absSessionKey(id)) },
			func(p *PebbleStore) error { return err1(p.ListABSSessionsForUser("u1")) }},
		{"ListAPIKeysForUser", "idx:apikey:user:u1:", indexOnly("idx:apikey:user:u1:"),
			func(id string) string { return "apikey:" + id },
			func(p *PebbleStore) error { return err1(p.ListAPIKeysForUser("u1")) }},
		{"ListDirtyUserPlaylists", "idx:upl:dirty:", indexOnly("idx:upl:dirty:"),
			func(id string) string { return "upl:" + id },
			func(p *PebbleStore) error { return err1(p.ListDirtyUserPlaylists()) }},
		{"ListUserBookStatesByStatus", "idx:ubs:status:u1:in_progress:", indexOnly("idx:ubs:status:u1:in_progress:"),
			func(id string) string { return "ubs:u1:" + id },
			func(p *PebbleStore) error { return err1(p.ListUserBookStatesByStatus("u1", "in_progress", 0, 0)) }},
		{"GetAllAuthorBookCounts", "book_authors:",
			func(t *testing.T, p *PebbleStore, id string) {
				require.NoError(t, p.db.Set([]byte("book_authors:"+id),
					[]byte(`[{"book_id":"`+id+`","author_id":1}]`), nil))
			},
			func(id string) string { return "book:" + id },
			func(p *PebbleStore) error { return err1(p.GetAllAuthorBookCounts()) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := setupTestPebbleStore(t)
			p.WaitForWarmup()
			p.UseMemDB = false // GetAllAuthorBookCounts takes its Pebble branch

			// A stale index entry (no record) is not-found and is skipped.
			tc.index(t, p, "stale1")
			require.NoError(t, tc.run(p), "a stale index entry must be skipped, not fail the list")

			// A read error on the index scan itself must fail the list.
			clear := setRowScanFault(p.db, tc.scanPrefix, 0, errInjectedScanFault)
			err := tc.run(p)
			clear()
			require.ErrorIs(t, err, errInjectedScanFault, "a truncated index scan must fail the list")

			// An unreadable member must fail the list, not shorten it.
			tc.index(t, p, "bad1")
			require.NoError(t, p.db.Set([]byte(tc.record("bad1")), []byte(corruptRow), nil))
			err = tc.run(p)
			require.Error(t, err, "an unreadable member must fail the list, not be skipped")
			require.ErrorContains(t, err, "bad1")
		})
	}
}
