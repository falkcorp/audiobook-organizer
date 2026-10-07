// file: internal/itunes/itl_le_repair_msdh_len_test.go
// version: 1.0.0
// guid: 8b4e2f19-6c3d-4a75-b0e1-2d9f7c5a3e68
// last-edited: 2026-10-07

package itunes

import (
	"testing"
)

// payloadWithTrailingMsdh builds a synthetic LE payload of n tracks plus one
// playlist referencing all of them, followed by a trailing msdh (type 3) whose
// TOTAL size is trailingTotal bytes. The trailing container is what sits after
// the playlist list in a real library (~2.3 KB there); its size is the
// threshold the old RepairITLDropDanglingMtphLE failed past.
func payloadWithTrailingMsdh(n, trailingTotal int) []byte {
	payload := buildManyTrackPayload(n)
	body := make([]byte, trailingTotal-fxMsdhHeaderLen)
	return append(payload, buildMsdh(3, body)...)
}

// TestRemoveTracksByPIDLE_PlaylistMsdhLenPastTrailingBytes is the regression
// for the 2026-10-07 playlist-length bug: after cutting the playlist mtph items
// of removed tracks, the repair looked up the type-2 msdh in the shortened
// buffer, its stale totalLen overran it, the lookup returned -1, and the msdh
// length was never decremented. container-tiling then rejected the write, so no
// removal touching more playlist bytes than the containers after the playlist
// list could ever land (~27 entries on the real libraries; 301 were needed).
//
// Synthetic payload only: a real .itl must never be committed (public repo).
func TestRemoveTracksByPIDLE_PlaylistMsdhLenPastTrailingBytes(t *testing.T) {
	const (
		nTracks       = 60
		trailingTotal = 4 * fxMtphHeaderLen // whole container = 4 mtph items: removing a 5th overran it
	)
	cases := []struct {
		name    string
		removed int
	}{
		{"below threshold", 2},
		{"exactly at threshold", 4},
		{"just past threshold", 5},
		{"far past threshold", 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := payloadWithTrailingMsdh(nTracks, trailingTotal)
			plBefore, _, plTotalBefore := findMsdhByType(before, 2)
			if plBefore < 0 {
				t.Fatal("fixture: playlist msdh not found")
			}

			pids := map[string]bool{}
			for i := range tc.removed {
				tid := uint32((i + 1) * 2)
				pid := pidForTID(before, tid)
				if pid == "" {
					t.Fatalf("fixture: no PID for tid %d", tid)
				}
				pids[pid] = true
			}

			after, n := RemoveTracksByPIDLE(before, pids)
			if n != tc.removed {
				t.Fatalf("removed %d tracks, want %d", n, tc.removed)
			}

			// The playlist msdh must still be locatable and its totalLen must
			// equal its real span: old length minus one mtph per removed track.
			plAfter, _, plTotalAfter := findMsdhByType(after, 2)
			if plAfter < 0 {
				t.Fatalf("playlist msdh no longer locatable after removing %d tracks: its totalLen was not decremented", tc.removed)
			}
			if want := plTotalBefore - tc.removed*fxMtphHeaderLen; plTotalAfter != want {
				t.Fatalf("playlist msdh totalLen = %d, want %d", plTotalAfter, want)
			}
			// The container after it must start exactly where the playlist list ends.
			if tr, _, trTotal := findMsdhByType(after, 3); tr != plAfter+plTotalAfter || trTotal != trailingTotal {
				t.Fatalf("trailing msdh at %d (len %d), want at %d (len %d)", tr, trTotal, plAfter+plTotalAfter, trailingTotal)
			}

			// The full contract, container-tiling included, accepts the result.
			v := RunSafetyContract(before, after, buildHeaderFor(after), DefaultContractConfig())
			for _, r := range v.Results {
				if r.Guard == "container-tiling" && !r.Pass() {
					t.Fatalf("container-tiling rejected the removal: %v", r.Violations)
				}
			}
			if !v.Pass {
				t.Fatalf("contract rejected the removal: %v\n%s", v.FailedGuards(), v.Error())
			}
		})
	}
}
