// file: core/updater/github/github_test.go
// version: 1.0.0
// guid: 3b8f1d62-9e07-4a45-b6c3-d70e5a14f928
// last-edited: 2026-11-18

package github_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/updater"
	"github.com/falkcorp/mediacore/updater/github"
)

// forge is a fake GitHub API: it serves ../testdata/release.json for the
// tag it is told about and records every request.
type forge struct {
	t    *testing.T
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	tags map[string]bool // tags that exist
}

func newForge(t *testing.T, tags ...string) *forge {
	t.Helper()
	f := &forge{t: t, tags: map[string]bool{}}
	for _, tag := range tags {
		f.tags[tag] = true
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *forge) body(tag string) string {
	raw, err := os.ReadFile("../testdata/release.json")
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.NewReplacer("{{TAG}}", tag, "{{BASE}}", f.srv.URL, "{{ASSET_NAME}}", "gotham-hq-linux-amd64").Replace(string(raw))
}

func (f *forge) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Clone(context.Background()))
	f.mu.Unlock()
	p := r.URL.Path
	switch {
	case p == "/repos/wayne/batcave/releases/latest":
		_, _ = io.WriteString(w, f.body("v0.224.0"))
	case strings.HasPrefix(p, "/repos/wayne/batcave/releases/tags/"):
		tag := strings.TrimPrefix(p, "/repos/wayne/batcave/releases/tags/")
		if !f.tags[tag] {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, f.body(tag))
	case p == "/repos/wayne/batcave/releases":
		_, _ = io.WriteString(w, "["+f.body("v0.225.0-rc.1")+"]")
	case strings.HasPrefix(p, "/assets/"):
		_, _ = io.WriteString(w, "the release bytes")
	default:
		http.NotFound(w, r)
	}
}

func (f *forge) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.URL.RequestURI())
	}
	return out
}

func TestGithubSource_ParameterisedSlugAndBaseURL(t *testing.T) {
	f := newForge(t, "v0.224.0")
	src := github.New("wayne", "batcave", github.BaseURL(f.srv.URL+"/"))

	rel, err := src.Latest(t.Context(), updater.ChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "0.224.0" || rel.URL != "https://example.invalid/releases/v0.224.0" || rel.Notes != "The Long Halloween" {
		t.Errorf("release = %+v", rel)
	}
	if len(rel.Assets) != 2 || rel.Assets[1].Name != "gotham-hq-linux-amd64" || rel.Assets[1].URL != f.srv.URL+"/assets/v0.224.0/gotham-hq-linux-amd64" {
		t.Errorf("assets = %+v", rel.Assets)
	}
	if got := f.paths(); len(got) != 1 || got[0] != "/repos/wayne/batcave/releases/latest" {
		t.Errorf("requests = %v, want the one latest lookup under the given slug and base URL", got)
	}

	// Another slug and base answer to their own paths: nothing is hard-wired.
	other := github.New("joker", "arkham", github.BaseURL(f.srv.URL))
	if _, err := other.Latest(t.Context(), updater.ChannelStable); !errors.Is(err, updater.ErrReleaseNotFound) {
		t.Errorf("another repository's latest = %v, want ErrReleaseNotFound from its own path", err)
	}
}

func TestGithubSource_PrereleaseChannelAndTags(t *testing.T) {
	f := newForge(t, "v0.224.0")
	src := github.New("wayne", "batcave", github.BaseURL(f.srv.URL))

	rel, err := src.Latest(t.Context(), updater.ChannelPrerelease)
	if err != nil || rel.Version != "0.225.0-rc.1" || rel.Channel != updater.ChannelPrerelease {
		t.Fatalf("prerelease channel: %+v, %v", rel, err)
	}

	ts, ok := src.(updater.TagSource)
	if !ok {
		t.Fatal("the github source is not a TagSource")
	}
	for _, v := range []string{"v0.224.0", "0.224.0"} {
		rel, err := ts.Release(t.Context(), v)
		if err != nil || rel.Version != "0.224.0" {
			t.Errorf("Release(%q) = %+v, %v", v, rel, err)
		}
	}
	if _, err := ts.Release(t.Context(), "v0.999.0"); !errors.Is(err, updater.ErrReleaseNotFound) {
		t.Errorf("an unknown tag = %v, want ErrReleaseNotFound", err)
	}
	before := len(f.paths())
	for _, bad := range []string{"", "latest", "v1.2", "v1.2.3/../x", "../v1.2.3", "v1.2.3?x=1", "v1.2.3#f", "https://example.invalid/v1.2.3", "v1.2.3\n"} {
		if _, err := ts.Release(t.Context(), bad); !errors.Is(err, updater.ErrInvalidVersion) {
			t.Errorf("Release(%q) = %v, want ErrInvalidVersion", bad, err)
		}
	}
	if got := len(f.paths()); got != before {
		t.Errorf("a malformed tag reached the network (%d requests)", got-before)
	}
}

func TestGithubSource_BadConfigurationFailsOnFirstUse(t *testing.T) {
	for name, src := range map[string]updater.Source{
		"empty owner":        github.New("", "batcave"),
		"owner with a slash": github.New("wayne/x", "batcave"),
		"repo with a space":  github.New("wayne", "bat cave"),
		"relative base":      github.New("wayne", "batcave", github.BaseURL("/api")),
		"scheme-less base":   github.New("wayne", "batcave", github.BaseURL("example.invalid")),
		"file base":          github.New("wayne", "batcave", github.BaseURL("file:///x")),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := src.Latest(t.Context(), updater.ChannelStable); err == nil {
				t.Error("Latest succeeded on a malformed source")
			}
			if _, _, err := src.Open(t.Context(), updater.Asset{URL: "https://example.invalid/x"}); err == nil {
				t.Error("Open succeeded on a malformed source")
			}
		})
	}
}

func TestGithubSource_BoundClientHeadersAndToken(t *testing.T) {
	f := newForge(t, "v0.224.0")
	secrets := 0
	env := updater.Env{
		HTTP: f.srv.Client(), UserAgent: "gotham-hq/v0.223.0",
		Secret: func(_ context.Context, name string) (string, error) {
			secrets++
			if name != "gotham/forge-token" {
				t.Errorf("secret name = %q", name)
			}
			return "batarang", nil
		},
	}
	src := github.New("wayne", "batcave", github.BaseURL(f.srv.URL), github.TokenSecret("gotham/forge-token"))
	b, ok := src.(updater.Binder)
	if !ok {
		t.Fatal("the github source is not a Binder")
	}
	bound := b.Bind(env)
	rel, err := bound.Latest(t.Context(), updater.ChannelStable)
	if err != nil {
		t.Fatal(err)
	}
	api := f.reqs[0]
	if api.Header.Get("User-Agent") != "gotham-hq/v0.223.0" {
		t.Errorf("api user agent = %q", api.Header.Get("User-Agent"))
	}
	if api.Header.Get("Authorization") != "Bearer batarang" {
		t.Errorf("api authorization = %q, want the bearer token", api.Header.Get("Authorization"))
	}
	if api.Header.Get("Accept") != "application/vnd.github.v3+json" {
		t.Errorf("api accept = %q", api.Header.Get("Accept"))
	}

	// The asset is on another host name than the API (127.0.0.1 vs localhost):
	// the token must not go with it.
	asset := rel.Assets[1]
	asset.URL = strings.Replace(asset.URL, "127.0.0.1", "localhost", 1)
	rc, size, err := bound.Open(t.Context(), asset)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(body) != "the release bytes" || size != int64(len(body)) {
		t.Errorf("asset body %q size %d", body, size)
	}
	dl := f.reqs[len(f.reqs)-1]
	if dl.Header.Get("Authorization") != "" {
		t.Error("the bearer token was sent to the asset host")
	}
	if dl.Header.Get("Accept") != "application/octet-stream" {
		t.Errorf("download accept = %q", dl.Header.Get("Accept"))
	}
	if secrets != 1 {
		t.Errorf("the secret was read %d times, want once (the API call only)", secrets)
	}

	// The unbound source is untouched by Bind.
	if _, err := src.Latest(t.Context(), updater.ChannelStable); err != nil {
		t.Fatalf("unbound Latest: %v", err)
	}
	if last := f.reqs[len(f.reqs)-1]; last.Header.Get("Authorization") != "" {
		t.Error("Bind leaked its env into the original source")
	}

	// A token the secrets store cannot give fails the call without the secret's name or value.
	failing := b.Bind(updater.Env{HTTP: f.srv.Client(), Secret: func(context.Context, string) (string, error) { return "", errors.New("vault sealed: batarang") }})
	_, err = failing.Latest(t.Context(), updater.ChannelStable)
	if err == nil || strings.Contains(err.Error(), "batarang") {
		t.Errorf("err = %v, want a failure that does not repeat the secret store's message", err)
	}
}

func TestGithubSource_OpenRefusesNonHTTPAndBadStatus(t *testing.T) {
	f := newForge(t)
	src := github.New("wayne", "batcave", github.BaseURL(f.srv.URL))
	for _, u := range []string{"file:///etc/passwd", "ftp://example.invalid/x", "/relative", ""} {
		if _, _, err := src.Open(t.Context(), updater.Asset{URL: u}); err == nil {
			t.Errorf("Open(%q) succeeded", u)
		}
	}
	if _, _, err := src.Open(t.Context(), updater.Asset{URL: f.srv.URL + "/missing"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("a 404 asset = %v, want a status error", err)
	}
}

func TestGithubSource_NotFoundLatestIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	src := github.New("wayne", "batcave", github.BaseURL(srv.URL))
	if _, err := src.Latest(t.Context(), updater.ChannelStable); !errors.Is(err, updater.ErrReleaseNotFound) {
		t.Errorf("err = %v, want ErrReleaseNotFound", err)
	}
	if _, err := src.Latest(t.Context(), updater.ChannelPrerelease); !errors.Is(err, updater.ErrReleaseNotFound) {
		t.Errorf("prerelease: err = %v, want ErrReleaseNotFound", err)
	}
	srv5 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(srv5.Close)
	if _, err := github.New("wayne", "batcave", github.BaseURL(srv5.URL)).Latest(t.Context(), updater.ChannelStable); err == nil || errors.Is(err, updater.ErrReleaseNotFound) {
		t.Errorf("a 500 = %v, want a real error", err)
	}
}
