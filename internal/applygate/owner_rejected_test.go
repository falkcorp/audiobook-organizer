// file: internal/applygate/owner_rejected_test.go
// version: 1.0.2
// guid: b82d75a5-b760-4375-98c3-d10426139c5d
// last-edited: 2026-10-10

package applygate

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// kvRejections is an in-memory rejection keyspace.
type kvRejections map[string][]byte

func (m kvRejections) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for k, v := range m {
		if strings.HasPrefix(k, prefix) {
			out = append(out, database.KVPair{Key: k, Value: v})
		}
	}
	return out, nil
}

// The owner-rejected leg refuses a rejected candidate before every other leg,
// fails closed on an unreadable list, and no owner review lifts either.
func TestEvaluate_OwnerRejectedLeg(t *testing.T) {
	book := &database.Book{ID: "b1", Title: "Synthetic Field Notes 1", Duration: intp(36000)}
	rt := database.ComputeBookRuntime(book, nil)
	c := metafetch.MetadataCandidate{Source: "Audible", Title: "Synthetic Field Notes 1", SeriesPosition: "1", Score: 0.95, DurationSec: 36000}
	if v := EvaluateInBatch(book, snap(book), rt, &c, nil, nil, &Rejections{}); !v.Allowed {
		t.Fatalf("fixture: an unrejected candidate is refused: %+v", v)
	}

	// A legacy rejection ("1") stored in another case than the candidate's.
	rejected := LoadRejections(kvRejections{metafetch.RejectedCandidateStoreKey("b1", "AUDIBLE", "synthetic field notes 1"): []byte("1")}, "b1")
	if rejected.ReadErr != nil {
		t.Fatal(rejected.ReadErr)
	}
	unreadable := &Rejections{ReadErr: errors.New("scan failed")}
	for _, tc := range []struct {
		name string
		rej  *Rejections
		want string
	}{
		{"rejected", rejected, ReasonOwnerRejected},
		{"unreadable", unreadable, ReasonOwnerRejectionCheckFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			low := c
			low.Score = 0.5 // a certainty refusal too: the rejection still reports first
			v := EvaluateInBatch(book, snap(book), rt, &low, nil, nil, tc.rej)
			if v.Allowed || v.Reason != tc.want {
				t.Fatalf("allowed=%v reason=%q, want refused %q", v.Allowed, v.Reason, tc.want)
			}
			if v.OwnerReviewOverridable() || v.UnseenOwnerReviewOverridable() {
				t.Fatal("an owner review must never lift an owner rejection")
			}
			if !slices.Contains(v.RefusingReasons(), tc.want) {
				t.Fatalf("RefusingReasons %v lacks %q", v.RefusingReasons(), tc.want)
			}
		})
	}
	if v := EvaluateInBatch(book, snap(book), rt, &c, nil, nil, nil); !v.Allowed {
		t.Fatalf("nil rejections must skip the leg: %+v", v)
	}
}
