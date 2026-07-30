// file: internal/audioutil/drm_test.go
// version: 1.1.0
// guid: f4be988f-5955-47d8-9379-ed828481fe27
// last-edited: 2026-07-30

package audioutil

import "testing"

// realFixturePaths are legitimate, DRM-free audio fixtures already committed
// to this repo. DetectDRM must never flag any of these as protected -- a
// false positive here would make a user's legitimate library unplayable,
// which is worse than the opaque-failure bug this task is fixing.
var realFixturePaths = []string{
	"../../testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4b",
	"../../testdata/audio/librivox/odyssey_butler_librivox/odyssey_01_homer_butler_64kb.mp3",
	"../../testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4a",
	"../../testdata/fixtures/test_sample.flac",
	"../../testdata/fixtures/test_sample.m4b",
	"../../testdata/fixtures/test_sample.mp3",
}

func TestDetectDRM_AAXExtension_Protected(t *testing.T) {
	cases := []string{
		"/any/path/book.aax",
		"/any/path/book.AAX",
		"/any/path/book.Aax",
		"/any/path/book.aAx",
	}
	for _, path := range cases {
		protected, reason := DetectDRM(path)
		if !protected {
			t.Errorf("DetectDRM(%q) protected = false, want true", path)
		}
		if reason != ReasonAudibleAAX {
			t.Errorf("DetectDRM(%q) reason = %q, want %q", path, reason, ReasonAudibleAAX)
		}
	}
}

func TestDetectDRM_AAXCExtension_Protected(t *testing.T) {
	cases := []string{
		"/any/path/book.aaxc",
		"/any/path/book.AAXC",
		"/any/path/book.Aaxc",
		"/any/path/book.aAxC",
	}
	for _, path := range cases {
		protected, reason := DetectDRM(path)
		if !protected {
			t.Errorf("DetectDRM(%q) protected = false, want true", path)
		}
		if reason != ReasonAudibleAAXC {
			t.Errorf("DetectDRM(%q) reason = %q, want %q", path, reason, ReasonAudibleAAXC)
		}
	}
}

func TestDetectDRM_RealFixtures_NotMisclassified(t *testing.T) {
	for _, path := range realFixturePaths {
		path := path
		t.Run(path, func(t *testing.T) {
			requireFixture(t, path)

			protected, reason := DetectDRM(path)
			if protected {
				t.Errorf("DetectDRM(%q) protected = true, want false", path)
			}
			if reason != "" {
				t.Errorf("DetectDRM(%q) reason = %q, want empty", path, reason)
			}
		})
	}
}

func TestDetectDRM_UnrelatedExtensions_NotProtected(t *testing.T) {
	// The full non-DRM list from config.AppConfig's default
	// SupportedExtensions, minus .aax/.aaxc. Synthetic paths -- this proves
	// the string-matching logic itself doesn't over-match (e.g. doesn't
	// accidentally treat ".m4a" as matching some ".aax"-adjacent check).
	extensions := []string{
		".m4b", ".mp3", ".m4a", ".aac", ".ogg", ".flac", ".wma",
		".opus", ".oga", ".wav", ".aiff", ".aif", ".mka",
	}
	for _, ext := range extensions {
		path := "/library/book" + ext
		protected, reason := DetectDRM(path)
		if protected {
			t.Errorf("DetectDRM(%q) protected = true, want false", path)
		}
		if reason != "" {
			t.Errorf("DetectDRM(%q) reason = %q, want empty", path, reason)
		}
	}
}

func TestDetectDRM_EmptyPath_NotProtected(t *testing.T) {
	protected, reason := DetectDRM("")
	if protected {
		t.Errorf("DetectDRM(\"\") protected = true, want false")
	}
	if reason != "" {
		t.Errorf("DetectDRM(\"\") reason = %q, want empty", reason)
	}
}

func TestDetectDRM_NoExtension_NotProtected(t *testing.T) {
	protected, reason := DetectDRM("/path/README")
	if protected {
		t.Errorf("DetectDRM(\"/path/README\") protected = true, want false")
	}
	if reason != "" {
		t.Errorf("DetectDRM(\"/path/README\") reason = %q, want empty", reason)
	}
}
