// file: internal/books/updater_options_test.go
// version: 0.1.0
// guid: 0d4a7f93-8b25-4c61-b3e0-5a92c1f68d47
// last-edited: 2026-11-22

//go:build unix

package books

// ABO's side of core's updater, moved from internal/updater (F8-04b 8b-4): the
// options core's updater is built with, the asset `updater fetch-release` asks
// a release for (F8-02 review M1), and what this app tells the release source
// about itself.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/updater"
	coreupdater "github.com/falkcorp/mediacore/updater"
	"github.com/falkcorp/mediacore/updater/github"
	"gopkg.in/yaml.v3"
)

// TestReleaseNamesMatchTheGinPath: internal/updater cannot import this package,
// so its gin path keeps its own copy of the repository, binary and app names
// (adapter.go); until that half is deleted the two sets are held equal here.
func TestReleaseNamesMatchTheGinPath(t *testing.T) {
	if updater.Owner != releaseOwner || updater.Repo != releaseRepo || updater.BinaryName != releaseBinary || updater.AppID != AppID {
		t.Fatalf("internal/updater names %s/%s %s %s, internal/books names %s/%s %s %s: the gin path and the wired updater would talk to different releases",
			updater.Owner, updater.Repo, updater.BinaryName, updater.AppID, releaseOwner, releaseRepo, releaseBinary, AppID)
	}
}

// TestABOUpdaterSourceMatchesHEAD pins what this app tells core's updater
// about itself: the repository the lookups go to, the request headers, the
// asset the stable channel downloads and the commit the develop channel reads.
// These were constants of internal/updater/updater.go before the move.
func TestABOUpdaterSourceMatchesHEAD(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			_, _ = w.Write([]byte(`{"tag_name":"v0.224.0","html_url":"https://example.invalid/r","body":"Year One","published_at":"2026-11-01T00:00:00Z","assets":[` +
				`{"name":"audiobook-organizer-` + runtime.GOOS + `-` + runtime.GOARCH + `","browser_download_url":"https://example.invalid/a","size":7}]}`))
		case strings.HasSuffix(r.URL.Path, "/commits/main"):
			_, _ = w.Write([]byte(`{"sha":"f00dfacecafe0123","html_url":"https://example.invalid/c","commit":{"message":"The Killing Joke\n\nbody","author":{"date":"2026-11-02T00:00:00Z"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	u := updater.NewUpdater("v0.223.0", updater.WithAPIBaseForTest(srv.URL), updater.WithHTTPClientForTest(srv.Client()), updater.WithStateDir(t.TempDir()))
	info, err := u.CheckForUpdate("stable")
	if err != nil {
		t.Fatal(err)
	}
	if info.LatestVersion != "0.224.0" || !info.UpdateAvailable || info.ReleaseURL != "https://example.invalid/r" ||
		info.ReleaseNotes != "Year One" || !info.PublishedAt.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("stable info = %+v", info)
	}
	dev, err := u.CheckForUpdate("develop")
	if err != nil {
		t.Fatal(err)
	}
	if dev.LatestVersion != "f00dfac" || dev.ReleaseNotes != "The Killing Joke" || !dev.UpdateAvailable || dev.Channel != "develop" {
		t.Errorf("develop info = %+v", dev)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"/repos/falkcorp/audiobook-organizer/releases/latest", "/repos/falkcorp/audiobook-organizer/commits/main"}
	if len(seen) != len(want) {
		t.Fatalf("requests = %d, want %d", len(seen), len(want))
	}
	for i, r := range seen {
		if r.URL.Path != want[i] {
			t.Errorf("request %d path = %q, want %q", i, r.URL.Path, want[i])
		}
		if got := r.Header.Get("User-Agent"); got != "audiobook-organizer/v0.223.0" {
			t.Errorf("request %d User-Agent = %q", i, got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github.v3+json" {
			t.Errorf("request %d Accept = %q", i, got)
		}
	}
}

// TestCoreOptions: what app.Options.Updater carries for this app.
func TestCoreOptions(t *testing.T) {
	o := CoreOptions()
	if o.AppID != AppID || o.BinaryName != "audiobook-organizer" || o.DefaultMode != coreupdater.ModeCheck {
		t.Errorf("CoreOptions = %+v", o)
	}
	if _, ok := o.Source.(coreupdater.TagSource); !ok {
		t.Error("the source cannot look a release up by tag, which `updater fetch-release` and a named apply need")
	}
	if _, ok := o.Source.(coreupdater.Binder); !ok {
		t.Error("the source takes no HTTP client or user agent from the updater")
	}
	// The default name is the one the gin apply has always asked for.
	if o.AssetName != nil {
		t.Error("CoreOptions names an asset: the gin apply must keep core's RawBinary default (HEAD's name)")
	}
	if got := coreupdater.RawBinary(o.BinaryName)(coreupdater.Release{}, "linux", "arm64"); got != "audiobook-organizer-linux-arm64" {
		t.Errorf("the release asset is %q, want audiobook-organizer-linux-arm64", got)
	}
}

// TestFetchOptions: what app.Options.Updater carries, which `updater
// fetch-release` reads: CoreOptions' app, with the asset name releases publish.
func TestFetchOptions(t *testing.T) {
	o := FetchOptions()
	c := CoreOptions()
	if o.AppID != c.AppID || o.BinaryName != c.BinaryName || o.DefaultMode != c.DefaultMode {
		t.Errorf("FetchOptions = %+v, CoreOptions = %+v", o, c)
	}
	if o.AssetName == nil {
		t.Fatal("FetchOptions has no asset namer")
	}
	if got := o.AssetName(coreupdater.Release{}, "linux", "arm64"); got != "audiobook-organizer_Linux_arm64" {
		t.Errorf("the fetched asset is %q, want audiobook-organizer_Linux_arm64", got)
	}
}

// TestReleasedAssetMatchesGoreleaser renders .goreleaser.yml's own binary
// template for every goos and goarch it builds and holds ReleasedAsset to the
// result, so a rename in the file fails here instead of in a rollback.
func TestReleasedAssetMatchesGoreleaser(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		ProjectName string `yaml:"project_name"`
		Builds      []struct {
			Binary string   `yaml:"binary"`
			Goos   []string `yaml:"goos"`
			Goarch []string `yaml:"goarch"`
		} `yaml:"builds"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Builds) != 1 {
		t.Fatalf(".goreleaser.yml has %d builds, this test reads one", len(cfg.Builds))
	}
	b := cfg.Builds[0]
	if len(b.Goos) == 0 || len(b.Goarch) == 0 {
		t.Fatalf("the build matrix is empty: %+v", b)
	}
	// With no project_name goreleaser names the project after the repository.
	project := cfg.ProjectName
	if project == "" {
		project = releaseRepo
	}
	tmpl, err := template.New("binary").Funcs(template.FuncMap{
		"title": func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
	}).Parse(b.Binary)
	if err != nil {
		t.Fatalf("the binary template: %v", err)
	}
	for _, goos := range b.Goos {
		for _, goarch := range b.Goarch {
			var out strings.Builder
			err := tmpl.Execute(&out, map[string]any{"ProjectName": project, "Os": goos, "Arch": goarch, "Arm": ""})
			if err != nil {
				t.Fatal(err)
			}
			if got := ReleasedAsset(coreupdater.Release{}, goos, goarch); got != out.String() {
				t.Errorf("ReleasedAsset(%s, %s) = %q, .goreleaser.yml publishes %q", goos, goarch, got, out.String())
			}
		}
	}
	// Spot values, so a template and a namer that drift together still show.
	for goos, want := range map[string]string{"linux": "audiobook-organizer_Linux_x86_64", "darwin": "audiobook-organizer_Darwin_x86_64"} {
		if got := ReleasedAsset(coreupdater.Release{}, goos, "amd64"); got != want {
			t.Errorf("ReleasedAsset(%s, amd64) = %q, want %q", goos, got, want)
		}
	}
	if got := ReleasedAsset(coreupdater.Release{}, "linux", "arm64"); got != "audiobook-organizer_Linux_arm64" {
		t.Errorf("ReleasedAsset(linux, arm64) = %q", got)
	}
}

// TestFetchOptions_FindsThePublishedAsset: a release publishing only
// goreleaser's names is fetched with FetchOptions and refused with
// CoreOptions, which stays on core's RawBinary name.
func TestFetchOptions_FindsThePublishedAsset(t *testing.T) {
	const tag = "v0.232.0"
	asset := ReleasedAsset(coreupdater.Release{}, runtime.GOOS, runtime.GOARCH)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"app\":%q,\"version\":\"0.232.0\",\"commit\":\"\",\"ui_build_id\":\"dev\",\"binary_id\":\"b1\"}'\n", AppID)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/falkcorp/audiobook-organizer/releases/tags/"+tag:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://example.invalid/r","body":"The Long Halloween","published_at":"2026-11-01T00:00:00Z","assets":[`+
				`{"name":%q,"browser_download_url":%q,"size":%d}]}`, tag, asset, srv.URL+"/dl/"+asset, len(script))
		case r.URL.Path == "/dl/"+asset:
			_, _ = w.Write([]byte(script))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	build := func(o coreupdater.Options) *coreupdater.Updater {
		o.Source = releaseSource(github.BaseURL(srv.URL), github.AllowInsecureHTTP())
		u, err := coreupdater.New(o, coreupdater.Deps{Log: slog.New(slog.DiscardHandler)}, coreupdater.WithVersion("v0.231.0"))
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	dest := filepath.Join(t.TempDir(), "fetched")
	if err := build(FetchOptions()).FetchRelease(t.Context(), tag, dest); err != nil {
		t.Fatalf("fetching %s with FetchOptions: %v", tag, err)
	}
	if fi, err := os.Stat(dest); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("fetched file: %v, %v", fi, err)
	}

	other := filepath.Join(t.TempDir(), "fetched")
	err := build(CoreOptions()).FetchRelease(t.Context(), tag, other)
	if err == nil || !strings.Contains(err.Error(), "audiobook-organizer-"+runtime.GOOS+"-"+runtime.GOARCH) {
		t.Errorf("CoreOptions fetch = %v, want the HEAD-named asset refused", err)
	}
	if _, statErr := os.Stat(other); statErr == nil {
		t.Error("a refused fetch left a file")
	}
}
