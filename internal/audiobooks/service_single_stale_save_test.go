// file: internal/audiobooks/service_single_stale_save_test.go
// version: 1.0.0
// guid: 727f8bc0-ddad-4dd8-b81c-2a3cb7624332
// last-edited: 2026-09-13

package audiobooks

import (
	"context"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// rowStore is a one-book store backed by a mutable row, so a test can change
// the row "concurrently" (from inside the ffprobe stand-in) and see what the
// GET writes back. UpdateBook fails the test: no read path may use it.
type rowStore struct {
	t     *testing.T
	mu    sync.Mutex
	row   database.Book
	fills []database.BookMediaInfoPatch
}

func (r *rowStore) store() *database.MockStore {
	return &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			cp := r.row
			return &cp, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			r.t.Errorf("read path called UpdateBook (whole-struct save) for %s", id)
			return b, nil
		},
		FillBookMediaInfoFunc: func(id string, p database.BookMediaInfoPatch) (*database.Book, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.fills = append(r.fills, p)
			// Same contract as PebbleStore: fill only what is still empty.
			if r.row.Duration == nil && p.Duration != nil {
				r.row.Duration = p.Duration
			}
			if r.row.Codec == nil && p.Codec != nil {
				r.row.Codec = p.Codec
			}
			if r.row.Bitrate == nil && p.Bitrate != nil {
				r.row.Bitrate = p.Bitrate
			}
			if r.row.SampleRate == nil && p.SampleRate != nil {
				r.row.SampleRate = p.SampleRate
			}
			if r.row.Channels == nil && p.Channels != nil {
				r.row.Channels = p.Channels
			}
			cp := r.row
			return &cp, nil
		},
	}
}

func stubMediaInfo(t *testing.T, fn func(string) (*mediainfo.MediaInfo, error)) {
	t.Helper()
	orig := extractMediaInfo
	extractMediaInfo = fn
	t.Cleanup(func() { extractMediaInfo = orig })
}

func ip(v int) *int       { return &v }
func sp(v string) *string { return &v }

// (a) Nothing derived differs from the stored row: zero writes of any kind.
func TestGetAudiobook_NothingChanged_NoWrites(t *testing.T) {
	rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "T", FilePath: "/nope/b1.m4b", Duration: ip(100)}}
	stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) {
		t.Error("ffprobe ran although duration is stored")
		return &mediainfo.MediaInfo{}, nil
	})
	svc := NewAudiobookService(rs.store())
	if _, err := svc.GetAudiobook(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if len(rs.fills) != 0 {
		t.Errorf("fills = %d, want 0", len(rs.fills))
	}
}

// (a) Missing duration, but the probe derives nothing: still zero writes.
func TestGetAudiobook_ProbeDerivesNothing_NoWrites(t *testing.T) {
	rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "T", FilePath: "/nope/b1.m4b"}}
	stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) { return &mediainfo.MediaInfo{}, nil })
	svc := NewAudiobookService(rs.store())
	if _, err := svc.GetAudiobook(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if len(rs.fills) != 0 {
		t.Errorf("fills = %d, want 0", len(rs.fills))
	}
}

// (a) Tags endpoint with every probed field stored: zero writes.
func TestGetAudiobookTags_NothingChanged_NoWrites(t *testing.T) {
	rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "T", FilePath: "/nope/b1.m4b",
		Codec: sp("aac"), Bitrate: ip(64), SampleRate: ip(44100), Channels: ip(2), Duration: ip(10)}}
	stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) {
		t.Error("ffprobe ran although media info is stored")
		return &mediainfo.MediaInfo{}, nil
	})
	svc := NewAudiobookService(rs.store())
	if _, err := svc.GetAudiobookTags(context.Background(), "b1", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(rs.fills) != 0 {
		t.Errorf("fills = %d, want 0", len(rs.fills))
	}
}

// (b) A metadata apply lands while ffprobe runs (between the GET's read and
// its save). The GET must neither revert it in the store nor serve or cache
// the pre-apply row.
func TestGetAudiobook_ConcurrentApplyNotReverted(t *testing.T) {
	rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "Old Title", FilePath: "/nope/b1.m4b"}}
	stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) {
		rs.mu.Lock()
		rs.row.Title = "Applied Title"
		rs.row.Narrator = sp("Applied Narrator")
		rs.mu.Unlock()
		return &mediainfo.MediaInfo{Duration: 3600}, nil
	})
	svc := NewAudiobookService(rs.store())
	got, err := svc.GetAudiobook(context.Background(), "b1")
	if err != nil {
		t.Fatal(err)
	}
	if rs.row.Title != "Applied Title" || rs.row.Narrator == nil || *rs.row.Narrator != "Applied Narrator" {
		t.Errorf("stored row reverted: title=%q narrator=%v", rs.row.Title, rs.row.Narrator)
	}
	if rs.row.Duration == nil || *rs.row.Duration != 3600 {
		t.Errorf("stored duration = %v, want 3600", rs.row.Duration)
	}
	if got.Title != "Applied Title" {
		t.Errorf("response title = %q, want the applied title", got.Title)
	}
	cached, err := svc.GetAudiobook(context.Background(), "b1")
	if err != nil || cached.Title != "Applied Title" {
		t.Errorf("cached title = %q (%v), want the applied title", cached.Title, err)
	}
}

// (b) Same race on the tags endpoint, including a media field the apply
// filled itself: the apply's value wins over the probe's.
func TestGetAudiobookTags_ConcurrentApplyNotReverted(t *testing.T) {
	rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "Old Title", FilePath: "/nope/b1.m4b"}}
	stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) {
		rs.mu.Lock()
		rs.row.Title = "Applied Title"
		rs.row.Codec = sp("opus")
		rs.mu.Unlock()
		return &mediainfo.MediaInfo{Codec: "aac", Bitrate: 64, SampleRate: 44100, Channels: 2, Duration: 10}, nil
	})
	svc := NewAudiobookService(rs.store())
	resp, err := svc.GetAudiobookTags(context.Background(), "b1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rs.row.Title != "Applied Title" {
		t.Errorf("stored title reverted to %q", rs.row.Title)
	}
	if *rs.row.Codec != "opus" {
		t.Errorf("stored codec = %q, want the apply's opus kept", *rs.row.Codec)
	}
	mi := resp["media_info"].(map[string]any)
	if mi["codec"] != "opus" {
		t.Errorf("response codec = %v, want the stored opus", mi["codec"])
	}
}

// (c) Only the fields that were empty and derived are sent, nothing else.
func TestGetAudiobookTags_WritesOnlyChangedFields(t *testing.T) {
	rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "T", FilePath: "/nope/b1.m4b",
		Codec: sp("aac"), Channels: ip(2), Duration: ip(10)}}
	stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) {
		return &mediainfo.MediaInfo{Codec: "mp3", Bitrate: 64, SampleRate: 44100, Channels: 1, Duration: 99}, nil
	})
	svc := NewAudiobookService(rs.store())
	if _, err := svc.GetAudiobookTags(context.Background(), "b1", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(rs.fills) != 1 {
		t.Fatalf("fills = %d, want 1", len(rs.fills))
	}
	p := rs.fills[0]
	if p.Codec != nil || p.Channels != nil || p.Duration != nil {
		t.Errorf("patch carries already-stored fields: %+v", p)
	}
	if p.Bitrate == nil || *p.Bitrate != 64 || p.SampleRate == nil || *p.SampleRate != 44100 {
		t.Errorf("patch = %+v, want bitrate 64 and sample rate 44100 only", p)
	}
}

// (c) GetAudiobook sends only the duration.
func TestGetAudiobook_WritesOnlyDuration(t *testing.T) {
	rs := &rowStore{t: t, row: database.Book{ID: "b1", Title: "T", FilePath: "/nope/b1.m4b"}}
	stubMediaInfo(t, func(string) (*mediainfo.MediaInfo, error) {
		return &mediainfo.MediaInfo{Codec: "mp3", Bitrate: 64, Duration: 42}, nil
	})
	svc := NewAudiobookService(rs.store())
	if _, err := svc.GetAudiobook(context.Background(), "b1"); err != nil {
		t.Fatal(err)
	}
	if len(rs.fills) != 1 {
		t.Fatalf("fills = %d, want 1", len(rs.fills))
	}
	want := database.BookMediaInfoPatch{Duration: rs.fills[0].Duration}
	if rs.fills[0] != want || *rs.fills[0].Duration != 42 {
		t.Errorf("patch = %+v, want duration 42 only", rs.fills[0])
	}
}
