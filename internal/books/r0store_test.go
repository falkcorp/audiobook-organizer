// file: internal/books/r0store_test.go
// version: 1.1.1
// guid: 5d8a1f36-2c7e-4b94-a0d3-6e9b4c1f7a28
// last-edited: 2026-11-09

package books

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/app"
)

// r0Tag is release R0, the production release OWNER-02 cut and kept
// (docs/plans/platform-core/STATE.md, W03's OWNER-02 row). R1's rollback is
// to this binary until the flip; after it, R0 must refuse the store
// (TestR0RefusesPostFlipStore).
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
const r0BinaryEnv = "AORG_TEST_R0_BINARY"

// r0Binary returns the path of the R0 binary for this platform: $AORG_TEST_R0_BINARY
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

// downloadAttempts and downloadBackoff bound download's retry of transient
// failures: 2s, 4s, 8s, 16s between five attempts.
const (
	downloadAttempts = 5
	downloadBackoff  = 2 * time.Second
)

// download fetches url to dst through a temp file renamed into place,
// retrying transient failures (network errors, 5xx, 429) with backoff.
func download(t *testing.T, url, dst string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	err := downloadWithRetry(ctx, t.Logf, http.DefaultClient, url, dst, downloadAttempts, downloadBackoff)
	require.NoError(t, err, "download the R0 release archive (set %s to use a local binary)", r0BinaryEnv)
}

// downloadWithRetry is download with the attempt count and first backoff
// injectable. The backoff doubles after each failed attempt. Any other 4xx
// fails at once: retrying cannot change the answer.
func downloadWithRetry(ctx context.Context, logf func(string, ...any), client *http.Client, url, dst string, attempts int, backoff time.Duration) error {
	for attempt := 1; ; attempt++ {
		transient, err := downloadOnce(ctx, client, url, dst)
		if err == nil {
			return nil
		}
		logf("download %s: attempt %d/%d failed: %v", url, attempt, attempts, err)
		if !transient || attempt >= attempts {
			return fmt.Errorf("download %s: %w", url, err)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return fmt.Errorf("download %s: waiting to retry: %w", url, ctx.Err())
		}
		backoff *= 2
	}
}

// downloadOnce makes one attempt. transient reports whether a failed attempt
// is worth retrying: a network or body-read error, a 5xx, or a 429.
func downloadOnce(ctx context.Context, client *http.Client, url, dst string) (transient bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return retry, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".r0-download-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		_ = tmp.Close()
		return true, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return false, os.Rename(tmp.Name(), dst)
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

// r0StorageFormat is the storage format R0 reads and writes
// (internal/database/storage_format.go at r0Tag: SupportedStorageFormat). R0's
// guarded open refuses a store whose stamp or sidecar names a higher one.
const r0StorageFormat = 1

// r0RefusalExit is R0's exit status when a command returns an error, a refused
// store open included: main prints the error to stderr and returns 1
// (main.go at r0Tag). R0 has no exit-78 path; 78 (kernel.ExitConfig) is what
// this tree's own pre-flip build answers, which TestCutoverRefusedByOlderBuilds
// pins.
const r0RefusalExit = 1

// r0Refusal is the error R0's guarded open returns for a store newer than it
// (StorageFormatTooNewError at r0Tag), from the stamp row or the sidecar.
var r0Refusal = regexp.MustCompile(`storage format (\d+) \((stamp|sidecar)\) in \S+audiobooks\.pebble is newer than this build supports \(1\); refusing to open`)

// runR0 runs the R0 binary with args against nothing of this machine's own:
// HOME, the state directory and the working directory are temp dirs, so R0
// reads no config file but the flags. It returns R0's combined output and exit
// status, and fails the test if R0 is still running at the deadline: a build
// that serves where it should refuse.
func runR0(t *testing.T, bin string, deadline time.Duration, args ...string) (string, int) {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = home
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + os.TempDir(), "ABK_STATE_DIR=" + filepath.Join(home, "state")}
	out, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "R0 %v was still running after %s: it did not refuse the store\n%s", args, deadline, out)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	require.NoError(t, err, "R0 %v did not run:\n%s", args, out)
	return string(out), 0
}

// TestR0RefusesPostFlipStore (05-storage D-S9, 5S.3.7; 09 §9.10): after the
// flip, the R0 release binary, swapped in over a store this build has
// migrated, refuses it: `audiobook-organizer serve` exits with R0's error
// status, names the reason (the store's storage format is newer than R0
// supports), and leaves every row and every file beside the store as the flip
// left them. The flip's level step raises books' level, which projects the
// legacy storage-format stamp and sidecar R0 reads to legacyMaxFormat+1. R0
// opening a store this build wrote was R1's rollback promise, void after the
// flip by design: the rollback is the pre-upgrade checkpoint plus the old
// binary (`make rollback-restore`), and a binary swap is refused. This cell is
// that refusal proved with R0's own binary, so the answer to "a refusal or a
// damaged store?" is measured, not assumed.
//
// Pebble's own files (tables, manifest, WAL, OPTIONS, LOCK) are not required
// byte-identical: R0's guarded open is pebble.Open first and the stamp check
// after it (openPebbleChecked at r0Tag), and pebble.Open may rewrite its own
// files without changing a row. Every row is compared through rawKVs, every
// other file under the store's directory through dirImage, and the Pebble files
// that did change are logged. R0 is run twice (a unit restarting it after the
// refusal), and this build then boots the store R0 refused: a refusal, not a
// damaged store.
//
// It needs the R0 binary: downloaded from the R0 release (network), or
// $AORG_TEST_R0_BINARY. Not run under -short.
func TestR0RefusesPostFlipStore(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the R0 release binary; not under -short")
	}
	bin := r0Binary(t)

	work := copyCorpus(t, r0Corpus(t))
	require.NoError(t, flipBootOn(t, work, false), "this build's flipped boot over the R0 corpus")
	flipLevel := flipOn().Manifest().Storage.Level
	require.Equal(t, itoa(flipLevel), levelsAt(t, work)["books"], "the flip did not raise books' level: the store is not post-flip and the cell proves nothing")
	require.True(t, r2StoreDone(t, storeDB(work), "books.identity-cutover")["books.identity-cutover"],
		"the flip did not record M3: the store's accounts are not core's and the cell proves nothing")
	sidecar, err := os.ReadFile(storeDB(work) + database.StorageFormatSidecarSuffix)
	require.NoError(t, err)
	require.Greater(t, atoi(t, strings.TrimSpace(string(sidecar))), r0StorageFormat,
		"the flipped store's storage-format sidecar is not above R0's format: R0 would open it")

	beforeRows, beforeFiles, beforePebble := rawKVs(t, storeDB(work)), dirImage(t, work), pebbleFileSums(t, storeDB(work))
	for run := 1; run <= 2; run++ {
		out, code := runR0(t, bin, 2*time.Minute, "serve", "--db", storeDB(work), "--dir", work, "--host", "127.0.0.1", "--port", "0")
		t.Logf("R0 serve, run %d, exited %d:\n%s", run, code, out)
		require.Equal(t, r0RefusalExit, code, "run %d: R0 did not refuse the post-flip store with its error status", run)
		m := r0Refusal.FindStringSubmatch(out)
		require.NotNil(t, m, "run %d: R0's output does not name the refusal (a store newer than R0 supports)", run)
		require.Greater(t, atoi(t, m[1]), r0StorageFormat, "run %d: R0 refused for a format it supports: %s", run, m[0])
		require.Equal(t, beforeRows, rawKVs(t, storeDB(work)), "run %d: R0's refused open changed the store's rows", run)
		require.Empty(t, imageDiff(beforeFiles, dirImage(t, work)), "run %d: R0's refused open wrote files beside the store", run)
	}
	t.Logf("Pebble files R0's refused opens changed (rows unchanged): %v", imageDiff(beforePebble, pebbleFileSums(t, storeDB(work))))

	// This build still serves the store R0 refused.
	require.NoError(t, bootAt(t, work, func(o *app.Options) { o.Env = bootEnv(absEnv()...) }),
		"this build does not boot the store R0 refused")
}

// pebbleFileSums is the SHA-256 of every file in the Pebble directory at path,
// by name.
func pebbleFileSums(t *testing.T, path string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	out := map[string]string{}
	for _, e := range entries {
		if e.Type().IsRegular() {
			out[e.Name()] = sha256File(t, filepath.Join(path, e.Name()))
		}
	}
	return out
}
