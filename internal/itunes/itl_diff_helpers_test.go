// file: internal/itunes/itl_diff_helpers_test.go
// version: 1.0.1
// guid: e2f3a4b5-c6d7-8e9f-0a1b-2c3d4e5f6a7b
// last-edited: 2026-08-20
//
// Tests for itl_diff_helpers.go: msdh container inventory and playlist
// membership diff. Fixtures are built using the existing synthetic LE
// builder helpers from itl_le_test.go (same package).

package itunes

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Fixture builders
// ---------------------------------------------------------------------------

// buildFixturePayload constructs a minimal LE payload with:
//   - msdh blockType=1 (track list) containing the given tracks
//   - msdh blockType=2 (playlist list) containing the given playlists
//
// Each track is a mith block followed by its mhoh children.
// Each playlist is a miph block followed by its mtph children.
func buildFixturePayload(tracks []fixtureTrk, playlists []fixturePL) []byte {
	// --- track-list container ---
	var trackContent []byte
	for _, tr := range tracks {
		mith := testBuildMithLE(tr.id, tr.pid, 0, 0)
		trackContent = append(trackContent, mith...)
	}
	trackMsdh := buildMsdhLE(0x01, trackContent)

	// --- playlist-list container ---
	var playlistContent []byte
	for _, pl := range playlists {
		miph := buildMiphLE(pl.pid)
		playlistContent = append(playlistContent, miph...)
		for _, tid := range pl.tids {
			mtph := buildMtphLE(tid)
			playlistContent = append(playlistContent, mtph...)
		}
	}
	playlistMsdh := buildMsdhLE(0x02, playlistContent)

	var payload []byte
	payload = append(payload, trackMsdh...)
	payload = append(payload, playlistMsdh...)
	return payload
}

type fixtureTrk struct {
	id  int
	pid [8]byte
}

type fixturePL struct {
	pid  [8]byte
	tids []int
}

// makeLibFromPayload parses a synthetic payload (no encryption, no compression).
func makeLibFromPayload(payload []byte) *ITLLibrary {
	lib := &ITLLibrary{}
	walkChunksLEImpl(payload, lib)
	return lib
}

// ---------------------------------------------------------------------------
// Tests: CollectMsdhInventory
// ---------------------------------------------------------------------------

func TestCollectMsdhInventory_Basic(t *testing.T) {
	payload := buildFixturePayload(
		[]fixtureTrk{{id: 1, pid: [8]byte{1}}},
		[]fixturePL{{pid: [8]byte{2}, tids: []int{1}}},
	)
	inv := CollectMsdhInventory(payload)
	if len(inv) != 2 {
		t.Fatalf("expected 2 msdh containers, got %d", len(inv))
	}
	if inv[0].BlockType != 1 {
		t.Errorf("first container: expected blockType=1, got %d", inv[0].BlockType)
	}
	if inv[1].BlockType != 2 {
		t.Errorf("second container: expected blockType=2, got %d", inv[1].BlockType)
	}
	// HeaderLen for our builder is always 16.
	for i, e := range inv {
		if e.HeaderLen != 16 {
			t.Errorf("inv[%d].HeaderLen: expected 16, got %d", i, e.HeaderLen)
		}
		if e.TotalLen < e.HeaderLen {
			t.Errorf("inv[%d].TotalLen %d < HeaderLen %d", i, e.TotalLen, e.HeaderLen)
		}
	}
}

func TestCollectMsdhInventory_NonLE(t *testing.T) {
	// Non-LE payload should return empty slice, not nil.
	inv := CollectMsdhInventory([]byte("not an LE payload at all"))
	if inv == nil {
		t.Fatal("expected non-nil empty slice for non-LE payload")
	}
	if len(inv) != 0 {
		t.Errorf("expected 0 entries for non-LE payload, got %d", len(inv))
	}
}

func TestCollectMsdhInventory_BlockTypeName(t *testing.T) {
	cases := []struct {
		bt   int
		want string
	}{
		{1, "track-list"},
		{2, "playlist-list"},
		{9, "album-list"},
		{11, "artist-list"},
		{99, "unknown"},
	}
	for _, c := range cases {
		e := MsdhEntry{BlockType: c.bt}
		if got := e.BlockTypeName(); got != c.want {
			t.Errorf("BlockType %d: expected %q, got %q", c.bt, c.want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: DiffMsdhInventory — fixture pair with one removed track
// ---------------------------------------------------------------------------

func TestDiffMsdhInventory_OneRemovedTrack(t *testing.T) {
	// Fixture A: 3 tracks, 1 playlist.
	payloadA := buildFixturePayload(
		[]fixtureTrk{{id: 1}, {id: 2}, {id: 3}},
		[]fixturePL{{pid: [8]byte{0xAA}, tids: []int{1, 2, 3}}},
	)
	// Fixture B: 2 tracks (track 3 removed), same playlist (minus track 3).
	payloadB := buildFixturePayload(
		[]fixtureTrk{{id: 1}, {id: 2}},
		[]fixturePL{{pid: [8]byte{0xAA}, tids: []int{1, 2}}},
	)

	diff := DiffMsdhInventory(payloadA, payloadB)

	// Both inventories have blockType 1 (track-list) and 2 (playlist-list).
	// At least one should show a changed total size (track list is smaller in B).
	if len(diff.OnlyA) != 0 {
		t.Errorf("expected no OnlyA containers, got %v", diff.OnlyA)
	}
	if len(diff.OnlyB) != 0 {
		t.Errorf("expected no OnlyB containers, got %v", diff.OnlyB)
	}
	// The track-list container (blockType=1) MUST have changed totalLen.
	found := false
	for _, ch := range diff.Changed {
		if ch.BlockType == 1 {
			found = true
			if ch.A.TotalLen <= ch.B.TotalLen {
				t.Errorf("track-list totalLen should be larger in A (3 tracks) than B (2 tracks): A=%d B=%d",
					ch.A.TotalLen, ch.B.TotalLen)
			}
		}
	}
	if !found {
		t.Error("expected blockType=1 (track-list) to appear in Changed after removing a track")
	}
}

func TestDiffMsdhInventory_Identical(t *testing.T) {
	payload := buildFixturePayload(
		[]fixtureTrk{{id: 1}, {id: 2}},
		[]fixturePL{{pid: [8]byte{0xBB}, tids: []int{1, 2}}},
	)
	diff := DiffMsdhInventory(payload, payload)
	if len(diff.Changed) != 0 || len(diff.OnlyA) != 0 || len(diff.OnlyB) != 0 {
		t.Errorf("identical payloads should produce empty diff, got: changed=%d onlyA=%d onlyB=%d",
			len(diff.Changed), len(diff.OnlyA), len(diff.OnlyB))
	}
}

// ---------------------------------------------------------------------------
// Tests: DiffPlaylistMembership — fixture pair with a playlist edit
// ---------------------------------------------------------------------------

func TestDiffPlaylistMembership_OnePlaylistEdit(t *testing.T) {
	// Both A and B share a playlist with PID {0xCC}.
	// A has TIDs [1,2,3], B has TIDs [1,3,4]: track 2 removed, track 4 added.
	pidPL := [8]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xCC}

	payloadA := buildFixturePayload(
		[]fixtureTrk{{id: 1}, {id: 2}, {id: 3}},
		[]fixturePL{{pid: pidPL, tids: []int{1, 2, 3}}},
	)
	payloadB := buildFixturePayload(
		[]fixtureTrk{{id: 1}, {id: 3}, {id: 4}},
		[]fixturePL{{pid: pidPL, tids: []int{1, 3, 4}}},
	)

	libA := makeLibFromPayload(payloadA)
	libB := makeLibFromPayload(payloadB)

	result := DiffPlaylistMembership(libA, libB)

	if len(result.OnlyA) != 0 {
		t.Errorf("expected no OnlyA playlists, got %v", result.OnlyA)
	}
	if len(result.OnlyB) != 0 {
		t.Errorf("expected no OnlyB playlists, got %v", result.OnlyB)
	}
	if len(result.Changed) != 1 {
		t.Fatalf("expected 1 changed playlist, got %d", len(result.Changed))
	}

	ch := result.Changed[0]
	if len(ch.Removed) != 1 || ch.Removed[0] != 2 {
		t.Errorf("expected Removed=[2], got %v", ch.Removed)
	}
	if len(ch.Added) != 1 || ch.Added[0] != 4 {
		t.Errorf("expected Added=[4], got %v", ch.Added)
	}
}

func TestDiffPlaylistMembership_PlaylistOnlyInA(t *testing.T) {
	pidA := [8]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xAA}
	pidB := [8]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xBB}

	payloadA := buildFixturePayload(
		[]fixtureTrk{{id: 1}},
		[]fixturePL{{pid: pidA, tids: []int{1}}, {pid: pidB, tids: []int{1}}},
	)
	// B only has playlist B.
	payloadB := buildFixturePayload(
		[]fixtureTrk{{id: 1}},
		[]fixturePL{{pid: pidB, tids: []int{1}}},
	)

	libA := makeLibFromPayload(payloadA)
	libB := makeLibFromPayload(payloadB)
	result := DiffPlaylistMembership(libA, libB)

	if len(result.OnlyA) != 1 {
		t.Fatalf("expected 1 OnlyA playlist, got %d", len(result.OnlyA))
	}
	if len(result.OnlyB) != 0 {
		t.Errorf("expected 0 OnlyB playlists, got %d", len(result.OnlyB))
	}
	if len(result.Changed) != 0 {
		t.Errorf("expected 0 Changed playlists, got %d", len(result.Changed))
	}
}

func TestDiffPlaylistMembership_Identical(t *testing.T) {
	payload := buildFixturePayload(
		[]fixtureTrk{{id: 1}, {id: 2}},
		[]fixturePL{{pid: [8]byte{0xDD}, tids: []int{1, 2}}},
	)
	lib := makeLibFromPayload(payload)
	result := DiffPlaylistMembership(lib, lib)
	if len(result.Changed) != 0 || len(result.OnlyA) != 0 || len(result.OnlyB) != 0 {
		t.Errorf("identical libraries should produce empty diff, got: changed=%d onlyA=%d onlyB=%d",
			len(result.Changed), len(result.OnlyA), len(result.OnlyB))
	}
}
