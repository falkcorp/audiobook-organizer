// file: internal/books/r2corpus_manifest_test.go
// version: 1.0.0
// guid: 80905133-ed00-49ea-8d25-a274c56a96b0
// last-edited: 2026-11-10

package books

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// r2corpusDir is R2's fixture corpus (F2-12): the store the R0 release binary
// wrote through its own API, its manifest and R0's recorded answers.
const r2corpusDir = "testdata/r2corpus"

// r2Manifest is manifest.json's format (coined by F2-12). F2-13 appends to
// Overlay and Blockers and adds its stores to Stores; stage-1 entries are never
// rewritten.
type r2Manifest struct {
	R0Tag          string                       `json:"r0_tag"`
	R0BinarySHA256 string                       `json:"r0_binary_sha256"`
	WrittenAt      string                       `json:"written_at"`
	Stage1         []r2Case                     `json:"stage1"`
	Overlay        []r2Case                     `json:"overlay"`
	Blockers       []r2Case                     `json:"blockers"`
	Stores         map[string][]r2ManifestEntry `json:"stores"`
	Answers        []r2Answer                   `json:"answers"`
}

type r2Case struct {
	Case   string `json:"case"`
	Family string `json:"family"`
	Step   string `json:"step"`
	How    string `json:"how"`
	Store  string `json:"store"`
}

type r2ManifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type r2Answer struct {
	Name string `json:"name"`
	File string `json:"file"`
	What string `json:"what"`
}

// repoRoot is two levels up from this package (internal/books). os.Getwd, not
// runtime.Caller: CI builds with -trimpath.
func r2RepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func loadR2Manifest(t *testing.T) r2Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r2corpusDir, "manifest.json"))
	require.NoError(t, err)
	var m r2Manifest
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// r0ReleaseTag reads ci/r0_release.txt by F3-08's reader rule: the one line
// that is neither blank nor starts with '#'.
func r0ReleaseTag(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "ci", "r0_release.txt"))
	require.NoError(t, err)
	var tags []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tags = append(tags, strings.TrimSpace(line))
	}
	require.Len(t, tags, 1, "ci/r0_release.txt must hold exactly one tag line")
	require.Regexp(t, regexp.MustCompile(`^v\d+\.\d+\.\d+$`), tags[0])
	return tags[0]
}

func r2FileSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	require.NoError(t, err)
	return hex.EncodeToString(h.Sum(nil))
}

// verifyStoreFiles asserts that the regular files under dir are exactly files:
// none missing, none extra, each with the listed size and sha256. Paths are
// slash-separated and relative to dir. F2-14's measurement test calls it on the
// store it copies.
func verifyStoreFiles(t *testing.T, dir string, files []r2ManifestEntry) {
	t.Helper()
	want := make(map[string]r2ManifestEntry, len(files))
	for _, f := range files {
		_, dup := want[f.Path]
		require.Falsef(t, dup, "%s: %s listed twice", dir, f.Path)
		want[f.Path] = f
	}
	var onDisk []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		onDisk = append(onDisk, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	sort.Strings(onDisk)

	seen := make(map[string]bool, len(onDisk))
	for _, rel := range onDisk {
		seen[rel] = true
		f, ok := want[rel]
		if !assert.Truef(t, ok, "%s: %s is on disk but not in the manifest", dir, rel) {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		assert.Equalf(t, f.Size, info.Size(), "%s: %s size", dir, rel)
		assert.Equalf(t, f.SHA256, r2FileSHA256(t, filepath.Join(dir, filepath.FromSlash(rel))), "%s: %s sha256", dir, rel)
	}
	for path := range want {
		assert.Truef(t, seen[path], "%s: %s is in the manifest but not on disk", dir, path)
	}
}

// TestR2Corpus_ManifestMatchesFiles checks R2's fixture corpus against its
// manifest (F2-12): every store an entry names exists and is listed, every
// listed file is on disk with its size and hash and nothing else is, stage 1
// holds a Pebble WAL or SST and the settings key, every answer file exists, and
// the manifest's R0 tag is the repository's.
func TestR2Corpus_ManifestMatchesFiles(t *testing.T) {
	root := r2RepoRoot(t)
	m := loadR2Manifest(t)

	assert.Equal(t, r0ReleaseTag(t, root), m.R0Tag, "manifest r0_tag vs ci/r0_release.txt")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), m.R0BinarySHA256)
	require.NotEmpty(t, m.Stage1, "stage 1 has no case")

	stage1Stores := map[string]bool{}
	for name, entries := range map[string][]r2Case{"stage1": m.Stage1, "overlay": m.Overlay, "blockers": m.Blockers} {
		for _, c := range entries {
			assert.NotEmptyf(t, c.Case, "%s entry without a case name", name)
			assert.NotEmptyf(t, c.Family, "%s case %q names no family", name, c.Case)
			if !assert.NotEmptyf(t, c.Store, "%s case %q names no store", name, c.Case) {
				continue
			}
			_, listed := m.Stores[c.Store]
			assert.Truef(t, listed, "%s case %q: store %s is not a key of stores", name, c.Case, c.Store)
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.Store)))
			if assert.NoErrorf(t, err, "%s case %q: store %s", name, c.Case, c.Store) {
				assert.Truef(t, info.IsDir(), "%s case %q: store %s is not a directory", name, c.Case, c.Store)
			}
			if name == "stage1" {
				stage1Stores[c.Store] = true
			}
		}
	}

	for store, files := range m.Stores {
		verifyStoreFiles(t, filepath.Join(root, filepath.FromSlash(store)), files)
	}

	for store := range stage1Stores {
		var walOrSST, encKey bool
		for _, f := range m.Stores[store] {
			base := filepath.Base(f.Path)
			if strings.HasSuffix(base, ".log") && strings.Contains(f.Path, ".pebble/") ||
				strings.HasSuffix(base, ".sst") {
				walOrSST = true
			}
			if base == ".encryption_key" {
				encKey = true
			}
		}
		assert.Truef(t, walOrSST, "stage-1 store %s lists no Pebble WAL (*.log) or SST (*.sst)", store)
		assert.Truef(t, encKey, "stage-1 store %s lists no .encryption_key", store)
	}

	require.GreaterOrEqual(t, len(m.Answers), 4, "effective config, schedules, search and ABS answers")
	for _, a := range m.Answers {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(a.File)))
		if !assert.NoErrorf(t, err, "answer %s", a.Name) {
			continue
		}
		var v map[string]any
		assert.NoErrorf(t, json.Unmarshal(raw, &v), "answer %s does not parse", a.Name)
	}
}
