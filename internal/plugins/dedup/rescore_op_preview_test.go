// file: internal/plugins/dedup/rescore_op_preview_test.go
// version: 1.0.0
// guid: 8d2f4a61-3c9e-4b75-a1d8-5e7c0b9f2a34
// last-edited: 2026-09-25

package dedup

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestRescoreOp_OmittedApplyPreviews: dedup.rescore rewrites the stored band
// of every pending candidate, and AutoResolveCertain acts on that band. Under
// the owner's 2026-09-25 rule, a request that does not say apply:true is a
// preview. runRescore used to pre-fill Apply:true before decoding, so `{}`,
// `null` and an empty body (a requeue) re-banded every row live.
//
// The config-PUT sink still gets a live run because it enqueues Apply:true
// explicitly (TestDedupScoreSink_SwapsLadderAndQueuesRescore pins that).
func TestRescoreOp_OmittedApplyPreviews(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	es := database.NewEmbeddingStore(pebble.DB())
	p := newCalibratePlugin(t, pebble, es)

	cases := []struct {
		name      string
		raw       json.RawMessage
		wantApply bool
	}{
		{"nil params", nil, false},
		{"empty object", json.RawMessage(`{}`), false},
		{"null", json.RawMessage(`null`), false},
		{"reason only", json.RawMessage(`{"reason":"hand run"}`), false},
		{"explicit apply", json.RawMessage(`{"apply":true}`), true},
		{"explicit preview", json.RawMessage(`{"apply":false}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := newCaptureReporter()
			if err := p.runRescore(context.Background(), tc.raw, rep); err != nil {
				t.Fatalf("runRescore: %v", err)
			}
			got, found := loggedRescoreApply(t, rep.buf.String())
			if !found {
				t.Fatalf("no \"dedup rescore complete\" log line; output:\n%s", rep.buf.String())
			}
			if got != tc.wantApply {
				t.Errorf("apply = %v, want %v", got, tc.wantApply)
			}
		})
	}
}

// loggedRescoreApply returns the apply field of the "dedup rescore complete"
// JSON log line.
func loggedRescoreApply(t *testing.T, out string) (bool, bool) {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var rec struct {
			Msg   string `json:"msg"`
			Apply *bool  `json:"apply"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Msg != "dedup rescore complete" || rec.Apply == nil {
			continue
		}
		return *rec.Apply, true
	}
	return false, false
}
