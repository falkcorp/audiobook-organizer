// file: internal/server/handlers/abs/search_book_cache_internal_test.go
// version: 1.0.0
// guid: 1d7f3a92-6c4b-4e08-b5a1-7e2c9f0d4b36
// last-edited: 2026-09-25

package abs

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

// Review finding 26: the ABS key uses the substring search's own fold and does
// not trim, so queries the store evaluates differently never share a list.
func TestAbsBookSearchKey(t *testing.T) {
	differ := [][2]string{{"_foo", "foo"}, {"foo_", "foo"}, {"rock ", "rock"}, {" rock", "rock"}}
	for _, p := range differ {
		if absBookSearchKey(p[0]) == absBookSearchKey(p[1]) {
			t.Errorf("%q and %q share a key", p[0], p[1])
		}
	}
	if absBookSearchKey("Odyssey_Two") != absBookSearchKey("odyssey two") {
		t.Error("the case and '_' fold the store applies should share a key")
	}
}

// Review finding 20: ABS clients cannot poll, so an ABS lookup never accepts a
// pending answer (which would render as zero hits).
func TestAbsLookupNeverPending(t *testing.T) {
	h := &Handler{bookSearch: searchcache.New(searchcache.NewChangeLog(0), searchcache.Config{})}
	if o := h.absLookup(); o.AllowPending || o.Wait <= 0 {
		t.Fatalf("absLookup allows pending: %+v", o)
	}
}
