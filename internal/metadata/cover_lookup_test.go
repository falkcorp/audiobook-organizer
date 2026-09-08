// file: internal/metadata/cover_lookup_test.go
// version: 1.0.0
// guid: b080c796-52a1-4a1e-a40d-232a50596515
// last-edited: 2026-09-08

package metadata

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// globCoverOracle is the implementation findExistingCover replaced, kept here as
// a test oracle. Any divergence between this and findExistingCover is either a
// regression or one of the two documented refinements (directories and dangling
// symlinks), which are asserted separately below.
func globCoverOracle(coversDir, id string) string {
	matches, _ := filepath.Glob(filepath.Join(coversDir, id+".*"))
	for _, m := range matches {
		ext := strings.ToLower(filepath.Ext(m))
		if ext == ".jpg" || ext == ".jpeg" || ext == ".png" || ext == ".webp" || ext == ".gif" {
			return m
		}
	}
	return ""
}

// TestFindExistingCover_AgreesWithGlobOracle walks every subset of the five
// known extensions plus two ignorable ones. The subsets matter more than any
// single case: precedence only shows up when a book has two covers on disk, and
// the old code's precedence came from Glob's sort order rather than from the
// order its if-statement listed the extensions in.
func TestFindExistingCover_AgreesWithGlobOracle(t *testing.T) {
	// Two extensions the lookup must ignore: one image-like, one not. Both were
	// ignored by the oracle because they are absent from the allow-list.
	all := []string{".gif", ".jpeg", ".jpg", ".png", ".webp", ".bmp", ".txt"}

	for mask := 0; mask < 1<<len(all); mask++ {
		dir := t.TempDir()
		coversDir := filepath.Join(dir, "covers")
		if err := os.MkdirAll(coversDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		var present []string
		for i, ext := range all {
			if mask&(1<<i) == 0 {
				continue
			}
			present = append(present, ext)
			p := filepath.Join(coversDir, "book"+ext)
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
		}

		want := globCoverOracle(coversDir, "book")
		got := findExistingCover(coversDir, "book")
		if got != want {
			t.Fatalf("present=%v: findExistingCover=%q, glob oracle=%q", present, got, want)
		}
	}
}

// TestFindExistingCover_PrecedenceIsAlphabetical pins the specific ordering fact
// that makes coverExtensions load-bearing. Written as an explicit case as well
// as via the oracle, so that reordering coverExtensions fails with a message
// that says why rather than as one row of a subset sweep.
func TestFindExistingCover_PrecedenceIsAlphabetical(t *testing.T) {
	if !sort.SliceIsSorted(coverExtensions[:], func(i, j int) bool {
		return coverExtensions[i] < coverExtensions[j]
	}) {
		t.Fatalf("coverExtensions must stay in ascending order to match the "+
			"precedence filepath.Glob produced by sorting its matches; got %v",
			coverExtensions)
	}

	coversDir := filepath.Join(t.TempDir(), "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// .gif sorts before .jpg, so the pre-existing behaviour serves the .gif even
	// though .jpg is the overwhelmingly common format in production.
	for _, ext := range []string{".gif", ".jpg"} {
		if err := os.WriteFile(filepath.Join(coversDir, "b"+ext), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if got, want := findExistingCover(coversDir, "b"), filepath.Join(coversDir, "b.gif"); got != want {
		t.Fatalf("precedence changed: got %q, want %q", got, want)
	}
}

func TestFindExistingCover_Misses(t *testing.T) {
	coversDir := filepath.Join(t.TempDir(), "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	t.Run("no file at all", func(t *testing.T) {
		if got := findExistingCover(coversDir, "absent"); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})

	t.Run("unknown extension is not a cover", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(coversDir, "odd.bmp"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := findExistingCover(coversDir, "odd"); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})

	t.Run("uppercase extension is not found", func(t *testing.T) {
		// Documents the deliberate lowercase-only contract. Writers always emit
		// lowercase (extensionFromContentType), so this can only arise from a
		// hand-placed file. Skipped on case-insensitive filesystems, where the
		// question is moot.
		p := filepath.Join(coversDir, "upper.JPG")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := os.Stat(filepath.Join(coversDir, "upper.jpg")); err == nil {
			t.Skip("filesystem is case-insensitive; lowercase probe already matches")
		}
		if got := findExistingCover(coversDir, "upper"); got != "" {
			t.Fatalf("want empty on a case-sensitive filesystem, got %q", got)
		}
	})

	t.Run("missing covers directory", func(t *testing.T) {
		if got := findExistingCover(filepath.Join(t.TempDir(), "nope"), "book"); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})
}

// TestFindExistingCover_NarrowerThanGlob covers the two cases where the stat
// probe deliberately returns less than the glob did. Both were unservable.
func TestFindExistingCover_NarrowerThanGlob(t *testing.T) {
	t.Run("directory named like a cover", func(t *testing.T) {
		coversDir := filepath.Join(t.TempDir(), "covers")
		if err := os.MkdirAll(filepath.Join(coversDir, "dir.jpg"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if got := findExistingCover(coversDir, "dir"); got != "" {
			t.Fatalf("a directory must not be served as a cover, got %q", got)
		}
		// The oracle did return it, which is the regression this guards against.
		if oracle := globCoverOracle(coversDir, "dir"); oracle == "" {
			t.Fatalf("oracle unexpectedly skipped the directory; the divergence " +
				"this test documents no longer exists")
		}
	})

	t.Run("dangling symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink creation is privileged on Windows")
		}
		coversDir := filepath.Join(t.TempDir(), "covers")
		if err := os.MkdirAll(coversDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		link := filepath.Join(coversDir, "gone.jpg")
		if err := os.Symlink(filepath.Join(coversDir, "target-does-not-exist"), link); err != nil {
			t.Skipf("symlink unsupported here: %v", err)
		}
		if got := findExistingCover(coversDir, "gone"); got != "" {
			t.Fatalf("a dangling symlink must not be served as a cover, got %q", got)
		}
	})
}

// TestFindExistingCover_RefusesEscapingID covers the SecureJoin guard directly,
// bypassing the callers' own sanitization. Without it, an id of "../<name>"
// would stat a file outside the covers directory and hand back its path.
func TestFindExistingCover_RefusesEscapingID(t *testing.T) {
	root := t.TempDir()
	coversDir := filepath.Join(root, "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A real file one level above the covers directory, i.e. the thing a
	// traversal would be reaching for.
	outside := filepath.Join(root, "secret.jpg")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := findExistingCover(coversDir, "../secret"); got != "" {
		t.Fatalf("escaping id resolved to %q; SecureJoin guard is not holding", got)
	}
	// Sanity check that the fixture is real: the same probe inside the covers
	// directory does find a file, so the empty result above is the guard and not
	// a broken test setup.
	if err := os.WriteFile(filepath.Join(coversDir, "inside.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := findExistingCover(coversDir, "inside"); got == "" {
		t.Fatal("fixture is inert: a cover inside coversDir was not found")
	}
}

func TestSafeCoverID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"01HZY", "01HZY"},
		{"../../etc/passwd", "passwd"},
		{"a/b/c", "c"},
		{"", ""},
		{".", ""},
		{"..", ""},
		{"/", ""},
		{"../..", ""},
	} {
		if got := safeCoverID(tc.in); got != tc.want {
			t.Errorf("safeCoverID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDownloadCoverArt_RejectsTraversingBookID pins the write side: a traversing
// bookID must be refused outright rather than writing through it.
func TestDownloadCoverArt_RejectsTraversingBookID(t *testing.T) {
	for _, bad := range []string{"..", ".", "/", "../.."} {
		_, err := downloadCoverArtWithClient(http.DefaultClient,
			"http://127.0.0.1:1/cover.jpg", t.TempDir(), bad)
		if err == nil {
			t.Fatalf("bookID %q was accepted; want rejection", bad)
		}
		if !strings.Contains(err.Error(), "invalid book ID") {
			t.Fatalf("bookID %q: got %v, want an invalid-book-ID rejection before any network use", bad, err)
		}
	}
}

// TestCoverPathForBook_SanitizesID keeps the traversal guard covered now that the
// lookup itself no longer goes through filepath.Glob.
func TestCoverPathForBook_SanitizesID(t *testing.T) {
	root := t.TempDir()
	coversDir := filepath.Join(root, "covers")
	if err := os.MkdirAll(coversDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(coversDir, "id.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// filepath.Base reduces the traversal to the final segment, which still
	// resolves to the real cover rather than escaping the covers directory.
	if got, want := CoverPathForBook(root, "../../id"), filepath.Join(coversDir, "id.jpg"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, bad := range []string{"", ".", "/", "..", "../.."} {
		if got := CoverPathForBook(root, bad); got != "" {
			t.Fatalf("CoverPathForBook(%q) = %q, want empty", bad, got)
		}
	}
}
