// file: internal/itunes/service/strip_chapter_prefix.go
// version: 2.1.0
// guid: 4d9e2f1a-7b6c-4e5f-8a3b-2c1d4e5f6a7b

package itunesservice

import "github.com/falkcorp/audiobook-organizer/internal/titleutil"

// stripChapterPrefix delegates to titleutil.StripChapterPrefix so the pattern
// list is maintained in one place. See titleutil for documentation and examples.
//
// Called by buildBookFromAlbumGroup ONLY when falling back from an empty
// Album tag to track.Name — when Album is present we trust it verbatim.
func stripChapterPrefix(title string) string {
	return titleutil.StripChapterPrefix(title)
}

// stripChapterSuffix delegates to titleutil.StripChapterSuffix. Used alongside
// stripChapterPrefix in the empty-Album fallback so per-chapter track names with
// a TRAILING part marker ("At All Costs – 11/23") collapse to one album key.
func stripChapterSuffix(title string) string {
	return titleutil.StripChapterSuffix(title)
}
