// file: internal/pathutil/versions_dir_test.go
// version: 1.0.0
// guid: e4834baf-c86f-418e-91f5-7a166ba09030
// last-edited: 2026-10-30

package pathutil

import "testing"

func TestUnderVersionsDir(t *testing.T) {
	const book = "/srv/library/Gotham/Batman Hush"
	for _, tc := range []struct {
		name         string
		path, folder string
		want         bool
	}{
		{"a slot file", book + "/.versions/v1/a.m4b", book, true},
		{"the .versions folder itself", book + "/.versions", book, true},
		{"a live file", book + "/a.m4b", book, false},
		{"a live file in a subfolder", book + "/CD2/a.m4b", book, false},
		{"a nested .versions", book + "/CD2/.versions/v1/a.m4b", book, true},
		{"similar name", book + "/.versions-old/a.m4b", book, false},
		{"the folder itself", book, book, false},
		{"outside the folder", "/srv/library/Gotham/Other/.versions/v1/a.m4b", book, false},
		{".versions above the folder is not a slot of it", "/srv/library/.versions/Hush/a.m4b", "/srv/library/.versions/Hush", false},
		{"sibling prefix", book + " 2/.versions/v1/a.m4b", book, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnderVersionsDir(tc.path, tc.folder); got != tc.want {
				t.Errorf("UnderVersionsDir(%q, %q) = %v, want %v", tc.path, tc.folder, got, tc.want)
			}
		})
	}
}

func TestHasSlotPair(t *testing.T) {
	for _, tc := range []struct {
		path, ver string
		want      bool
	}{
		{"/b/.versions/v1/a.m4b", "v1", true},
		{"/b/.versions/v1", "v1", true},
		{"/b/.versions/v2/a.m4b", "v1", false},
		{"/b/v1/.versions/a.m4b", "v1", false},
		{"/b/.versions/v1/a.m4b", "", false},
		{"/b/x/../.versions/v1/a.m4b", "v1", true},
	} {
		if got := HasSlotPair(tc.path, tc.ver); got != tc.want {
			t.Errorf("HasSlotPair(%q, %q) = %v, want %v", tc.path, tc.ver, got, tc.want)
		}
	}
}
