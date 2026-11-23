// file: internal/books/updater_apply_test.go
// version: 0.1.0
// guid: 7c31e9a4-2d58-4b07-9f6a-e04b1d83a5c2
// last-edited: 2026-11-22

//go:build unix

package books

// The core updater as the flip wires it (F8-04b): built from FetchOptions with
// the release source pointed at a test server, with only a Restarter in its
// Deps (Deps.HTTP and Deps.Notify nil, the I10 fallback, F7-09 not merged).
// Apply is HEAD's pipeline: one raw asset, goreleaser's name for it (owner
// ruling O-5, I5), no archive, no checksums file, no backup.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks/taskstest"
	coreupdater "github.com/falkcorp/mediacore/updater"
	"github.com/falkcorp/mediacore/updater/github"
)

const (
	applyTag     = "v0.232.0"
	applyRunning = "v0.231.0"
	applyOld     = "the previous Batmobile"
)

// applyRestarter records each restart the updater asked for.
type applyRestarter struct {
	mu      sync.Mutex
	reasons []string
}

func (r *applyRestarter) Restart(_ context.Context, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
	return nil
}

// applyRig is the wired updater over a fake GitHub that lists the release's
// assets and serves each one, recording every download path asked of it.
type applyRig struct {
	u    *coreupdater.Updater
	rst  *applyRestarter
	exe  string
	dir  string
	mu   sync.Mutex
	hits []string
}

func (r *applyRig) downloaded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.hits)
}

// newApplyRig builds the updater from FetchOptions, the options the flip hands
// the kernel, with Source replaced by ABO's own source over srv. The release
// lists the goreleaser asset for this OS and arch, plus an archive and a
// checksums file that Apply must never ask for.
func newApplyRig(t *testing.T) *applyRig {
	t.Helper()
	asset := ReleasedAsset(coreupdater.Release{}, runtime.GOOS, runtime.GOARCH)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"app\":%q,\"version\":\"0.232.0\",\"commit\":\"\",\"ui_build_id\":\"dev\",\"binary_id\":\"b1\"}'\n", AppID)
	r := &applyRig{rst: &applyRestarter{}, dir: t.TempDir()}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/repos/falkcorp/audiobook-organizer/releases/tags/"+applyTag:
			w.Header().Set("Content-Type", "application/json")
			base := srv.URL + "/dl/"
			_, _ = fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example.invalid/r","body":"The Dark Knight Returns","published_at":"2026-11-01T00:00:00Z","assets":[`+
				`{"name":%q,"browser_download_url":%q,"size":%d},`+
				`{"name":"audiobook-organizer_0.232.0_Linux_x86_64.tar.gz","browser_download_url":%q,"size":1},`+
				`{"name":"checksums.txt","browser_download_url":%q,"size":1}]}`,
				applyTag, asset, base+asset, len(script), base+"archive.tar.gz", base+"checksums.txt")
		case req.URL.Path == "/dl/"+asset:
			r.mu.Lock()
			r.hits = append(r.hits, req.URL.Path)
			r.mu.Unlock()
			_, _ = w.Write([]byte(script))
		default:
			r.mu.Lock()
			r.hits = append(r.hits, req.URL.Path)
			r.mu.Unlock()
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)

	r.exe = filepath.Join(r.dir, "audiobook-organizer")
	if err := os.WriteFile(r.exe, []byte(applyOld), 0o755); err != nil {
		t.Fatal(err)
	}
	o := FetchOptions()
	o.Source = releaseSource(github.BaseURL(srv.URL), github.AllowInsecureHTTP())
	// Deps carries a Restarter and nothing else: HTTP and Notify are nil.
	u, err := coreupdater.New(o, coreupdater.Deps{Restarter: r.rst, Log: slog.New(slog.DiscardHandler)},
		coreupdater.WithVersion(applyRunning),
		coreupdater.WithExecutableForTest(func() (string, error) { return r.exe, nil }),
		coreupdater.WithStateDir(t.TempDir()),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.u = u
	return r
}

// TestApply_RawAssetNoBackup: through the options the flip wires, the one
// asset an apply fetches is goreleaser's raw executable. No archive and no
// checksums file are asked for, <exe> becomes the release, the previous binary
// is kept as <exe>.prev, and nothing else (no backup, no download left behind)
// is written beside it.
func TestApply_RawAssetNoBackup(t *testing.T) {
	r := newApplyRig(t)
	if err := r.u.Apply(taskstest.NewFakeRun(t.Context(), coreupdater.KindApply).RC(), applyTag); err != nil {
		t.Fatalf("Apply(%s): %v", applyTag, err)
	}
	want := "/dl/" + ReleasedAsset(coreupdater.Release{}, runtime.GOOS, runtime.GOARCH)
	if got := r.downloaded(); !slices.Equal(got, []string{want}) {
		t.Fatalf("download requests = %v, want exactly [%s]", got, want)
	}
	if b, err := os.ReadFile(r.exe + ".prev"); err != nil || string(b) != applyOld {
		t.Errorf("<exe>.prev = %q, %v; want the previous binary", b, err)
	}
	if b, err := os.ReadFile(r.exe); err != nil || string(b) == applyOld {
		t.Errorf("<exe> = %q, %v; want the release", b, err)
	}
	ents, err := os.ReadDir(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if want := []string{"audiobook-organizer", "audiobook-organizer.prev"}; !slices.Equal(names, want) {
		t.Errorf("beside <exe>: %v, want %v (no .new, no .old, no backup)", names, want)
	}
}

// TestApply_NilHTTPAndNotify (I10): the updater built with Deps.HTTP and
// Deps.Notify nil, which is how the flip wires it until F7-09, applies a
// release over the source's own client, asks the supervisor for exactly one
// restart, and does not panic on either hook.
func TestApply_NilHTTPAndNotify(t *testing.T) {
	r := newApplyRig(t)
	if err := r.u.Apply(taskstest.NewFakeRun(t.Context(), coreupdater.KindApply).RC(), applyTag); err != nil {
		t.Fatalf("Apply with no HTTP client and no notify service: %v", err)
	}
	r.rst.mu.Lock()
	defer r.rst.mu.Unlock()
	if len(r.rst.reasons) != 1 || r.rst.reasons[0] == "" {
		t.Errorf("restarts = %q, want exactly one with a reason", r.rst.reasons)
	}
}
