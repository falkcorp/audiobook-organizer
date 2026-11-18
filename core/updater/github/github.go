// file: core/updater/github/github.go
// version: 1.0.0
// guid: b27e4d90-5a13-4c68-8f0e-93d1c7a6e254
// last-edited: 2026-11-18

// Package github is the GitHub releases source of the core updater
// (05-updater 5U.3): HEAD's release lookups, parameterised by the repository
// and the API root, so no repository is named in core.
package github

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/updater"
)

// DefaultBaseURL is the GitHub REST API root.
const DefaultBaseURL = "https://api.github.com"

// maxAPIBody bounds one release or release-list answer.
const maxAPIBody = 8 << 20

type config struct {
	baseURL     string
	tokenSecret string
}

// Option configures New.
type Option func(*config)

// BaseURL points the source at a GitHub Enterprise or API-compatible forge.
func BaseURL(u string) Option { return func(c *config) { c.baseURL = strings.TrimRight(u, "/") } }

// TokenSecret names a core/secrets entry whose value is sent as a bearer token
// to the API host (never to another host, and never as a value in config).
func TokenSecret(name string) Option { return func(c *config) { c.tokenSecret = name } }

// source implements updater.Source, updater.TagSource and updater.Binder.
type source struct {
	owner, repo string
	cfg         config
	cfgErr      error
	env         updater.Env
	client      *http.Client
}

var (
	_ updater.Source    = (*source)(nil)
	_ updater.TagSource = (*source)(nil)
	_ updater.Binder    = (*source)(nil)
)

// New returns a Source for owner/repo. A malformed owner, repo or BaseURL is
// reported by the source's first call, not here, so New keeps the shape
// 05-updater 5U.3 gives it.
func New(owner, repo string, opts ...Option) updater.Source {
	c := config{baseURL: DefaultBaseURL}
	for _, o := range opts {
		o(&c)
	}
	s := &source{owner: owner, repo: repo, cfg: c, client: defaultClient()}
	switch {
	case !slugPart().MatchString(owner) || !slugPart().MatchString(repo):
		s.cfgErr = errors.New("github source: owner and repository must be non-empty names of letters, digits, '.', '_' and '-'")
	default:
		u, err := url.Parse(c.baseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			s.cfgErr = errors.New("github source: BaseURL must be an absolute http(s) URL")
		}
	}
	return s
}

func slugPart() *regexp.Regexp { return regexp.MustCompile(`^[A-Za-z0-9._-]+$`) }

// tagPattern is the only shape a tag may have when it is put into a release
// lookup URL: "v", three numeric parts, optional pre-release and build parts.
func tagPattern() *regexp.Regexp {
	return regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
}

// Bind implements updater.Binder: a copy of the source that sends through
// env's HTTP client with env's user agent and secret reader.
func (s *source) Bind(env updater.Env) updater.Source {
	c := *s
	c.env = env
	return &c
}

func (s *source) httpClient() *http.Client {
	if s.env.HTTP != nil {
		return s.env.HTTP
	}
	return s.client
}

// defaultClient is the client of a source nothing bound: no overall Timeout (a
// release binary is large, and the caller's context bounds the transfer), only
// the wait for the response headers.
func defaultClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: t}
}

func (s *source) apiURL(format string, args ...any) string {
	return s.cfg.baseURL + fmt.Sprintf(format, args...)
}

// request builds a GET for target with the headers HEAD sent. The bearer
// token goes only to the API host.
func (s *source) request(ctx context.Context, target, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Accept", accept)
	if s.env.UserAgent != "" {
		req.Header.Set("User-Agent", s.env.UserAgent)
	}
	if s.cfg.tokenSecret != "" && s.env.Secret != nil && sameHost(target, s.cfg.baseURL) {
		tok, err := s.env.Secret(ctx, s.cfg.tokenSecret)
		if err != nil {
			return nil, errors.New("the release source token could not be read")
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return req, nil
}

func sameHost(a, b string) bool {
	ua, errA := url.Parse(a)
	ub, errB := url.Parse(b)
	return errA == nil && errB == nil && strings.EqualFold(ua.Host, ub.Host)
}

type apiRelease struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	} `json:"assets"`
}

// get GETs target and decodes its JSON answer. found is false on a 404.
func (s *source) get(ctx context.Context, target string, out any) (found bool, err error) {
	req, err := s.request(ctx, target, "application/vnd.github.v3+json")
	if err != nil {
		return false, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to check for updates: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("release API returned status %d", resp.StatusCode)
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, maxAPIBody), out); err != nil {
		return false, fmt.Errorf("failed to decode the release answer: %w", err)
	}
	return true, nil
}

func (r apiRelease) release(ch updater.Channel) updater.Release {
	out := updater.Release{
		Version: strings.TrimPrefix(r.TagName, "v"),
		Channel: ch,
		Notes:   r.Body,
		URL:     r.HTMLURL,
	}
	if r.Prerelease {
		out.Channel = updater.ChannelPrerelease
	}
	if r.PublishedAt != "" {
		out.PublishedAt, _ = time.Parse(time.RFC3339, r.PublishedAt)
	}
	for _, a := range r.Assets {
		out.Assets = append(out.Assets, updater.Asset{Name: a.Name, URL: a.BrowserDownloadURL, Size: a.Size})
	}
	return out
}

// Latest implements updater.Source: the stable channel is the repository's
// latest release; the prerelease channel is its newest published release,
// prerelease or not.
func (s *source) Latest(ctx context.Context, ch updater.Channel) (updater.Release, error) {
	if s.cfgErr != nil {
		return updater.Release{}, s.cfgErr
	}
	if ch == updater.ChannelPrerelease {
		var list []apiRelease
		found, err := s.get(ctx, s.apiURL("/repos/%s/%s/releases?per_page=10", s.owner, s.repo), &list)
		if err != nil {
			return updater.Release{}, err
		}
		if found {
			for _, r := range list {
				if !r.Draft && r.TagName != "" {
					return r.release(ch), nil
				}
			}
		}
		return updater.Release{}, updater.ErrReleaseNotFound
	}
	var r apiRelease
	found, err := s.get(ctx, s.apiURL("/repos/%s/%s/releases/latest", s.owner, s.repo), &r)
	if err != nil {
		return updater.Release{}, err
	}
	if !found {
		return updater.Release{}, updater.ErrReleaseNotFound
	}
	return r.release(ch), nil
}

// Release implements updater.TagSource: the release tagged version.
func (s *source) Release(ctx context.Context, version string) (updater.Release, error) {
	if s.cfgErr != nil {
		return updater.Release{}, s.cfgErr
	}
	tag := version
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if !tagPattern().MatchString(tag) || strings.Contains(tag, "..") {
		return updater.Release{}, updater.ErrInvalidVersion
	}
	var r apiRelease
	found, err := s.get(ctx, s.apiURL("/repos/%s/%s/releases/tags/%s", s.owner, s.repo, url.PathEscape(tag)), &r)
	if err != nil {
		return updater.Release{}, err
	}
	if !found {
		return updater.Release{}, fmt.Errorf("%w: %s", updater.ErrReleaseNotFound, tag)
	}
	return r.release(updater.ChannelStable), nil
}

// Open implements updater.Source: it streams one asset. The caller closes it.
func (s *source) Open(ctx context.Context, a updater.Asset) (io.ReadCloser, int64, error) {
	if s.cfgErr != nil {
		return nil, -1, s.cfgErr
	}
	if u, err := url.Parse(a.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, -1, errors.New("the asset URL is not an absolute http(s) URL")
	}
	req, err := s.request(ctx, a.URL, "application/octet-stream")
	if err != nil {
		return nil, -1, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, -1, fmt.Errorf("failed to download update: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, -1, fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	return resp.Body, resp.ContentLength, nil
}
