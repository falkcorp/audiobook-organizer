// file: internal/metafetch/service_apply_cover_test.go
// version: 1.0.0
// guid: e2a71c84-5d03-4f19-b7a6-1c40e9d3b825
// last-edited: 2026-08-15

package metafetch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRenderableCoverURL is the regression test for a blank cover on apply.
//
// The UI serves covers through /api/v1/covers/proxy, which rejects hosts outside
// its allow-list — production returned 400 "URL not from an allowed cover source"
// for m.media-amazon.com. ApplyMetadataToBook writes the candidate's REMOTE url
// into book.CoverURL, which was harmless only because the cover download ran
// inline and replaced it microseconds later. Once the download moved to the
// background, that remote url became observable and the cover rendered BLANK
// until a refresh after the download landed.
func TestRenderableCoverURL(t *testing.T) {
	ptr := func(s string) *string { return &s }
	const remote = "https://m.media-amazon.com/images/I/51Iuc4TUM2L._SL500_.jpg"
	const localOld = "/api/v1/covers/local/01ABC.jpg"

	tests := []struct {
		name      string
		previous  *string
		applied   *string
		candidate string
		want      *string
		why       string
	}{
		{
			name:      "remote url is replaced by the previous local cover",
			previous:  ptr(localOld),
			applied:   ptr(remote),
			candidate: remote,
			want:      ptr(localOld),
			why:       "the existing image must keep rendering until the new one is on disk",
		},
		{
			name:      "no previous cover leaves it unset rather than remote",
			previous:  nil,
			applied:   ptr(remote),
			candidate: remote,
			want:      nil,
			why:       "persisting an unrenderable url is worse than none; the background download fills it in",
		},
		{
			name:      "candidate without a cover leaves the applied value alone",
			previous:  ptr(localOld),
			applied:   ptr(localOld),
			candidate: "",
			want:      ptr(localOld),
			why:       "nothing to defer; do not disturb the existing value",
		},
		{
			name:      "an applied value that is not the remote url is kept",
			previous:  ptr(localOld),
			applied:   ptr("/api/v1/covers/local/NEW.jpg"),
			candidate: remote,
			want:      ptr("/api/v1/covers/local/NEW.jpg"),
			why:       "a local path already on disk must not be reverted to the old cover",
		},
		{
			name:      "nil applied stays nil",
			previous:  ptr(localOld),
			applied:   nil,
			candidate: remote,
			want:      nil,
			why:       "no value was applied; nothing to correct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderableCoverURL(tt.previous, tt.applied, tt.candidate)
			if tt.want == nil {
				assert.Nil(t, got, tt.why)
				return
			}
			if assert.NotNil(t, got, tt.why) {
				assert.Equal(t, *tt.want, *got, tt.why)
			}
		})
	}
}

// TestRenderableCoverURL_NeverReturnsTheRemoteURL is the property that matters,
// stated directly: whatever the inputs, this function must never hand back the
// candidate's remote url, because that is the value the UI cannot render.
func TestRenderableCoverURL_NeverReturnsTheRemoteURL(t *testing.T) {
	ptr := func(s string) *string { return &s }
	const remote = "https://m.media-amazon.com/images/I/x.jpg"

	previouses := []*string{nil, ptr("/api/v1/covers/local/old.jpg"), ptr(remote)}
	applieds := []*string{nil, ptr(remote), ptr("/api/v1/covers/local/new.jpg")}

	for _, prev := range previouses {
		for _, app := range applieds {
			got := renderableCoverURL(prev, app, remote)
			// The one legitimate way remote can come back is if the PREVIOUS value
			// was already remote, i.e. it was persisted before this fix existed.
			if got != nil && *got == remote && (prev == nil || *prev != remote) {
				t.Fatalf("returned the unrenderable remote url (previous=%v applied=%v)", prev, app)
			}
		}
	}
}
