// file: internal/updater/fetch_asset_test.go
// version: 1.0.0
// guid: 9d4b7e21-5a38-4c06-8f13-2e60b1a97c54
// last-edited: 2026-11-18

//go:build unix

package updater

// The asset `updater fetch-release` asks a release for (F8-02 review M1): the
// raw-binary name .goreleaser.yml publishes, not the name HEAD's gin apply
// asked for, which no release has ever carried.

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"

	coreupdater "github.com/falkcorp/mediacore/updater"
	"github.com/falkcorp/mediacore/updater/github"
	"gopkg.in/yaml.v3"
)

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
		project = Repo
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
// CoreOptions, which stays on HEAD's name (the gin apply is unchanged).
func TestFetchOptions_FindsThePublishedAsset(t *testing.T) {
	const tag = "v0.232.0"
	asset := ReleasedAsset(coreupdater.Release{}, runtime.GOOS, runtime.GOARCH)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"app\":%q,\"version\":\"0.232.0\",\"commit\":\"\",\"ui_build_id\":\"dev\",\"binary_id\":\"b1\"}'\n", AppID)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/tags/"+tag):
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
		o.Source = NewSource(github.BaseURL(srv.URL), github.AllowInsecureHTTP())
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
