// file: internal/deluge/discovery_test.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567891
// last-edited: 2026-09-01
//
// Tests for the four-tier discovery matching helpers in internal/deluge/discovery.go.

package deluge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testDlDir = "/mnt/bigdata/books/deluge"

// ---------------------------------------------------------------------------
// NormalizeTitle
// ---------------------------------------------------------------------------

func TestNormalizeTitle_Basic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"The Way of Kings", "the way of kings"},
		{"The Way of Kings!", "the way of kings"},
		{"Dune (2023)", "dune 2023"},
		{"Foundation - Isaac Asimov", "foundation isaac asimov"},
		{"", ""},
		{"  spaces  ", "spaces"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, NormalizeTitle(c.in), "NormalizeTitle(%q)", c.in)
	}
}

// ---------------------------------------------------------------------------
// IsPathTracked (Tier 2)
// ---------------------------------------------------------------------------

func TestIsPathTracked_EmptyContentPath(t *testing.T) {
	known := map[string]struct{}{testDlDir + "/Dune/Dune.m4b": {}}
	assert.False(t, IsPathTracked("", known))
}

func TestIsPathTracked_ExactMatch(t *testing.T) {
	known := map[string]struct{}{testDlDir + "/Dune.m4b": {}}
	assert.True(t, IsPathTracked(testDlDir+"/Dune.m4b", known))
}

func TestIsPathTracked_ContentDirPrefixMatch(t *testing.T) {
	known := map[string]struct{}{testDlDir + "/Dune/Dune.m4b": {}}
	assert.True(t, IsPathTracked(testDlDir+"/Dune", known))
}

func TestIsPathTracked_UnimportedTorrent(t *testing.T) {
	known := map[string]struct{}{testDlDir + "/Dune/Dune.m4b": {}}
	assert.False(t, IsPathTracked(testDlDir+"/Foundation", known))
}

func TestIsPathTracked_PartialNameNotMatched(t *testing.T) {
	// "Du" must NOT match "Dune" — we check full path prefix with trailing /
	known := map[string]struct{}{testDlDir + "/Dune/Dune.m4b": {}}
	assert.False(t, IsPathTracked(testDlDir+"/Du", known))
}

func TestIsPathTracked_TrailingSlashNormalized(t *testing.T) {
	known := map[string]struct{}{testDlDir + "/Dune/Dune.m4b": {}}
	assert.True(t, IsPathTracked(testDlDir+"/Dune/", known))
}

// ---------------------------------------------------------------------------
// ParseTorrentNameCandidates (Tier 3 helper)
// ---------------------------------------------------------------------------

func TestParseTorrentNameCandidates_DashSeparated(t *testing.T) {
	candidates := ParseTorrentNameCandidates("Brandon Sanderson - The Way of Kings")
	assert.Contains(t, candidates, NormalizeTitle("Brandon Sanderson"))
	assert.Contains(t, candidates, NormalizeTitle("The Way of Kings"))
}

func TestParseTorrentNameCandidates_ByKeyword(t *testing.T) {
	candidates := ParseTorrentNameCandidates("The Way of Kings by Brandon Sanderson [M4B]")
	assert.Contains(t, candidates, NormalizeTitle("The Way of Kings"))
}

func TestParseTorrentNameCandidates_DotSeparated(t *testing.T) {
	candidates := ParseTorrentNameCandidates("Dune.Frank.Herbert.2023.M4B")
	assert.Contains(t, candidates, "dune frank herbert")
}

func TestParseTorrentNameCandidates_UnabridgedStripped(t *testing.T) {
	candidates := ParseTorrentNameCandidates("Dune [Unabridged]")
	// "unabridged" must be stripped; only "dune" remains
	for _, c := range candidates {
		assert.NotContains(t, c, "unabridged")
	}
	assert.Contains(t, candidates, "dune")
}

// ---------------------------------------------------------------------------
// IsTitleTracked (Tier 3)
// ---------------------------------------------------------------------------

func TestIsTitleTracked_Hit(t *testing.T) {
	titles := map[string]struct{}{
		NormalizeTitle("The Way of Kings"): {},
	}
	assert.True(t, IsTitleTracked("Brandon Sanderson - The Way of Kings [M4B]", titles))
}

func TestIsTitleTracked_Miss(t *testing.T) {
	titles := map[string]struct{}{
		NormalizeTitle("Dune"): {},
	}
	assert.False(t, IsTitleTracked("Brandon Sanderson - The Way of Kings", titles))
}

func TestIsTitleTracked_EmptyTitles(t *testing.T) {
	assert.False(t, IsTitleTracked("Brandon Sanderson - The Way of Kings", map[string]struct{}{}))
}

// ---------------------------------------------------------------------------
// SHA256File + IsContentHashTracked (Tier 4)
// ---------------------------------------------------------------------------

func TestSHA256File_BasicHash(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.m4b")
	require.NoError(t, os.WriteFile(f, []byte("audiodata"), 0o644))

	hash1, err := SHA256File(f)
	require.NoError(t, err)
	assert.Len(t, hash1, 64) // hex-encoded SHA256 = 64 chars

	// Same content → same hash.
	hash2, err := SHA256File(f)
	require.NoError(t, err)
	assert.Equal(t, hash1, hash2)
}

func TestSHA256File_DifferentContent(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.m4b")
	f2 := filepath.Join(dir, "b.m4b")
	require.NoError(t, os.WriteFile(f1, []byte("data1"), 0o644))
	require.NoError(t, os.WriteFile(f2, []byte("data2"), 0o644))

	h1, _ := SHA256File(f1)
	h2, _ := SHA256File(f2)
	assert.NotEqual(t, h1, h2)
}

func TestSHA256File_MissingFile(t *testing.T) {
	_, err := SHA256File("/nonexistent/file.m4b")
	assert.Error(t, err)
}

func TestIsContentHashTracked_MatchFound(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "book.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audiodata"), 0o644))

	expected, _ := SHA256File(audio)
	lookup := func(h string) bool { return h == expected }

	assert.True(t, IsContentHashTracked(dir, lookup))
}

func TestIsContentHashTracked_NoMatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "book.m4b"), []byte("audiodata"), 0o644))

	lookup := func(h string) bool { return false }
	assert.False(t, IsContentHashTracked(dir, lookup))
}

func TestIsContentHashTracked_SkipsNonAudioFiles(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("text"), 0o644))

	called := false
	lookup := func(h string) bool { called = true; return true }
	assert.False(t, IsContentHashTracked(dir, lookup))
	assert.False(t, called, "lookup should not be called for non-audio files")
}

func TestIsContentHashTracked_MissingDir(t *testing.T) {
	lookup := func(h string) bool { return true }
	assert.False(t, IsContentHashTracked("/nonexistent/path", lookup))
}

// ---------------------------------------------------------------------------
// AudioExtensions
// ---------------------------------------------------------------------------

// This test binary never calls config.InitConfig, so AppConfig.SupportedExtensions
// is nil here — which is exactly the fail-open case audioext.Resolve exists for.
// If the fallback ever regressed to "empty set", this test would go red rather
// than the discovery walk silently classifying every torrent as untracked.
func TestAudioExtensions_ContainsExpectedFormats(t *testing.T) {
	expected := []string{".m4b", ".m4a", ".mp3", ".flac", ".aax", ".aac", ".ogg", ".opus", ".wav",
		// Previously absent from the private list this set replaced.
		".aaxc", ".aiff", ".aif", ".mka", ".oga", ".wma"}
	got := AudioExtensions()
	for _, ext := range expected {
		assert.True(t, got.Has(ext), "AudioExtensions should contain %q", ext)
	}
}
