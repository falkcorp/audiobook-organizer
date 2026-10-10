// file: internal/server/handlers/metadata/candidate_checks_owner_rejected_test.go
// version: 1.0.0
// guid: 4b7e2d91-6c3a-4f08-a5d2-8e1f0c9b3a67
// last-edited: 2026-10-10

package metadatahandler

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// memRejectionKV is an in-memory rejection keyspace.
type memRejectionKV map[string][]byte

func (m memRejectionKV) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for k, v := range m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, database.KVPair{Key: k, Value: v})
		}
	}
	return out, nil
}

// The dialog marks a rejected candidate, also one rejected under another
// source by its ASIN, and puts no rejection text in Detail: the chip
// renders from the flag, so an Undo that clears the flag leaves nothing
// stale behind (review round 3, N3). Synthetic fixtures.
func TestCheckCandidate_OwnerRejectedFlagOnly(t *testing.T) {
	kv := memRejectionKV{
		metafetch.RejectedCandidateStoreKey("b1", "Audible", "Synthetic Field Notes 8"): []byte(`{"ids":["asin:b0synchk01"]}`),
	}
	rejected, err := metafetch.LoadRejectedCandidates(kv, "b1")
	if err != nil {
		t.Fatal(err)
	}
	book := &database.Book{ID: "b1", Title: "Synthetic Field Notes 8"}
	for _, c := range []metafetch.MetadataCandidate{
		{Source: "Audible", Title: "Synthetic Field Notes 8", ASIN: "B0SYNCHK01"},
		{Source: "Audnexus (Audible)", Title: "Synthetic Field Notes 8", ASIN: "B0SYNCHK01"},
	} {
		got := checkCandidate(book, nil, rejected, &c)
		if !got.OwnerRejected {
			t.Fatalf("%s: not marked owner-rejected", c.Source)
		}
		if got.Detail != "" {
			t.Fatalf("%s: Detail = %q, want no rejection text", c.Source, got.Detail)
		}
	}
	other := metafetch.MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 8", ASIN: "B0SYNCHK02"}
	if checkCandidate(book, nil, rejected, &other).OwnerRejected {
		t.Fatal("another ASIN of the title was marked rejected")
	}
}
