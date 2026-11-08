// file: internal/books/r0store_test.go
// version: 1.0.0
// guid: 5d8a1f36-2c7e-4b94-a0d3-6e9b4c1f7a28
// last-edited: 2026-11-08

package books

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// r0Tag is release R0, the production release OWNER-02 cut and kept
// (docs/plans/platform-core/STATE.md, W03's OWNER-02 row). R1's rollback is
// to this binary. R3-02 deletes this test with the HEAD-format readers.
const r0Tag = "v0.223.0"

// r0ArchiveSHA256 pins each R0 release archive to the checksum the release's
// checksums.txt lists, so a moved or re-uploaded asset fails the test instead
// of running some other binary.
var r0ArchiveSHA256 = map[string]string{
	"Darwin_arm64":  "5cf771484551c67a43660435657b6754f658cf862a581deb971f9d3090617322",
	"Darwin_x86_64": "270f10a6ce5c5e1360b39b538ceff45ebf75e665fa03fa99034ee480a7380f9a",
	"Linux_arm64":   "447233c3d7058dfe18ca3aff1c0187df972a89ddb0801e1574f9c95a42535485",
	"Linux_x86_64":  "acbe9e096b04d80bd1a49d8e0ecc1bf7dc8171e004ea26cf795513e881ba1e6d",
}

// r0BinaryEnv names a prebuilt R0 binary to use instead of the download:
// for a machine with no network, or to try a candidate R0 build.
const r0BinaryEnv = "ABO_R0_BINARY"

// r0Binary returns the path of the R0 binary for this platform: $ABO_R0_BINARY
// when set, else the release archive downloaded from the R0 release (cached
// under the user cache directory, its checksum checked on every use) and the
// binary extracted from it into a temp dir. It fails, never skips, when the
// download fails: a rollback that cannot be checked is not a passing one.
func r0Binary(t *testing.T) string {
	t.Helper()
	if p := os.Getenv(r0BinaryEnv); p != "" {
		return p
	}
	osName := map[string]string{"darwin": "Darwin", "linux": "Linux"}[runtime.GOOS]
	arch := map[string]string{"amd64": "x86_64", "arm64": "arm64"}[runtime.GOARCH]
	platform := osName + "_" + arch
	want, ok := r0ArchiveSHA256[platform]
	if !ok {
		t.Skipf("R0 has no release binary for %s/%s; set %s to run this test", runtime.GOOS, runtime.GOARCH, r0BinaryEnv)
	}
	version := strings.TrimPrefix(r0Tag, "v")
	archive := fmt.Sprintf("audiobook-organizer_%s_%s.tar.gz", version, platform)

	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		cacheRoot = os.TempDir()
	}
	cacheDir := filepath.Join(cacheRoot, "audiobook-organizer-tests", "r0", r0Tag)
	require.NoError(t, os.MkdirAll(cacheDir, 0o700))
	cached := filepath.Join(cacheDir, archive)
	if sha256File(t, cached) != want {
		url := fmt.Sprintf("https://github.com/falkcorp/audiobook-organizer/releases/download/%s/%s", r0Tag, archive)
		download(t, url, cached)
		got := sha256File(t, cached)
		require.Equal(t, want, got, "the R0 archive %s does not match its pinned checksum", archive)
	}

	bin := filepath.Join(t.TempDir(), "audiobook-organizer-r0")
	extractTarEntry(t, cached, "audiobook-organizer_"+platform, bin)
	return bin
}

// sha256File is the hex SHA-256 of the file at path; "" when it is absent.
func sha256File(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	h := sha256.New()
	_, err = io.Copy(h, f)
	require.NoError(t, err)
	return hex.EncodeToString(h.Sum(nil))
}

// download fetches url to dst through a temp file renamed into place.
func download(t *testing.T, url, dst string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "download the R0 release archive (set %s to use a local binary)", r0BinaryEnv)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode, "download %s", url)
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".r0-download-*")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = io.Copy(tmp, resp.Body)
	require.NoError(t, err)
	require.NoError(t, tmp.Close())
	require.NoError(t, os.Rename(tmp.Name(), dst))
}

// extractTarEntry writes the regular file named name in the .tar.gz at
// archive to dst, executable.
func extractTarEntry(t *testing.T, archive, name, dst string) {
	t.Helper()
	f, err := os.Open(archive)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			t.Fatalf("%s holds no %s", archive, name)
		}
		require.NoError(t, err)
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != name {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		require.NoError(t, err)
		_, err = io.Copy(out, tr) //nolint:gosec // a pinned-checksum release archive
		require.NoError(t, err)
		require.NoError(t, out.Close())
		return
	}
}

// runR0 runs the R0 binary with args against nothing of this machine's own:
// HOME, the state directory and the working directory are temp dirs, so R0
// reads no config file but the flags.
func runR0(t *testing.T, bin string, args ...string) string {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = home
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir(), "ABK_STATE_DIR=" + filepath.Join(home, "state")}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "R0 %v failed:\n%s", args, out)
	return string(out)
}

// r0RawRows parses R0's `diagnostics query --raw` output: each key with its
// value's length.
func r0RawRows(t *testing.T, out string) map[string]int {
	t.Helper()
	rows := map[string]int{}
	var key string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if k, ok := strings.CutPrefix(line, "Key: "); ok {
			key = k
			continue
		}
		if n, ok := strings.CutPrefix(line, "Value length: "); ok && key != "" {
			v, err := strconv.Atoi(strings.TrimSuffix(n, " bytes"))
			require.NoError(t, err, "R0 output line %q", line)
			rows[key] = v
			key = ""
		}
	}
	require.NoError(t, sc.Err())
	return rows
}

// TestR0OpensR1Store (05-storage; 09 §9.10, §9.4 R1 Rollback): the R0
// release binary opens a store release R1 wrote and reads every row. The
// store is a HEAD store upgraded by ABO's R1 boot (the first-boot
// checkpoint, core's rows, books' R1 migrations, the settings load's
// writes, the boot repairs) with a book R1 wrote while serving. R0 reads
// every key and value through Pebble, opens the store through its own
// guarded open and lists every book, and after that open every row R1
// left is still there: `make rollback` to R0 loses nothing.
//
// It needs the R0 binary: downloaded from the R0 release (network), or
// $ABO_R0_BINARY. Not run under -short.
func TestR0OpensR1Store(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the R0 release binary; not under -short")
	}
	bin := r0Binary(t)

	path := filepath.Join(tempDir(t), "gotham.pebble")
	seed := headStoreAt(t, path, nil)
	edit, disk := onDisk(path)
	r := runComposedWith(t, edit, nil, disk, nil)
	served, err := r.store.CreateBook(&database.Book{Title: "The Long Halloween", FilePath: filepath.Join(filepath.Dir(path), "library", "The Long Halloween.m4b")})
	require.NoError(t, err)
	require.NoError(t, r.stop())

	r1 := rawKVs(t, path)
	require.NotEmpty(t, keysWithPrefix(r1, "c/"), "the R1 boot wrote no core row: the test proves nothing")
	want := map[string]int{}
	for k, v := range r1 {
		want[k] = len(v)
	}

	raw := runR0(t, bin, "diagnostics", "query", "--raw", "--prefix=", "--limit", strconv.Itoa(len(r1)+1000), "--db", path)
	assert.Equal(t, want, r0RawRows(t, raw), "R0 does not read the rows R1 left")

	listed := runR0(t, bin, "diagnostics", "query", "--limit", "1000", "--db", path)
	for _, id := range []string{seed.bookID, served.ID} {
		assert.Contains(t, listed, "ID: "+id, "R0's store open does not list book %s", id)
	}

	after := rawKVs(t, path)
	for k := range r1 {
		_, ok := after[k]
		assert.True(t, ok, "R0's open lost row %q", k)
	}
}
