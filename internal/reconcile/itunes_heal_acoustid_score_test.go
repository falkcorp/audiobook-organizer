// file: internal/reconcile/itunes_heal_acoustid_score_test.go
// version: 1.0.0
// guid: 5b9e2c07-3f14-4a86-8d71-c0a4f6e2b153
// last-edited: 2026-09-19

package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/acoustid"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// TestResolveAmbiguousByAcoustID_WeakScoreIgnored: the AcoustID resolver
// ranks candidates by the matched recording title. A match below
// AcoustIDOnlineMinScore (0.85) is noise and must not pick a winner; the
// control at 0.95 proves the fixture does resolve on a strong match.
func TestResolveAmbiguousByAcoustID_WeakScoreIgnored(t *testing.T) {
	for _, tc := range []struct {
		score float64
		want  string
	}{{0.50, ""}, {0.95, "/a.m4b"}} {
		t.Run(fmt.Sprintf("score_%.2f", tc.score), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				title := "Unrelated Thing"
				if r.FormValue("duration") == "600" {
					title = "The Silent Harbor Mystery"
				}
				fmt.Fprintf(w, `{"status":"ok","results":[{"id":"x","score":%v,"recordings":[{"id":"mb","title":%q,"artists":[{"name":"Jane Narrator"}]}]}]}`, tc.score, title)
			}))
			defer srv.Close()
			ac := acoustid.NewClient("K")
			ac.BaseURL = srv.URL

			raw := make([]byte, 400*4)
			for i := range raw {
				raw[i] = byte(i * 7)
			}
			m := &database.MockStore{}
			m.GetBookFileByPathFunc = func(p string) (*database.BookFile, error) {
				dur := 600.0
				if p == "/b.m4b" {
					dur = 601
				}
				return &database.BookFile{ID: p, FilePath: p, AcoustIDFingerprint: raw,
					AcoustIDFingerprintDurationSec: dur, AcoustIDFPVersion: fingerprint.PrintEncodingVersion}, nil
			}
			track := iTunesTrack{Album: "The Silent Harbor Mystery", Artist: "Jane Narrator"}
			got := resolveAmbiguousByAcoustID(context.Background(), m, ac, track, []string{"/a.m4b", "/b.m4b"}, nil)
			if got != tc.want {
				t.Fatalf("score %.2f resolved to %q, want %q", tc.score, got, tc.want)
			}
		})
	}
}
