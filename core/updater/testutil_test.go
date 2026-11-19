// file: core/updater/testutil_test.go
// version: 1.1.0
// guid: e41b8c07-3d92-4a56-9c1f-0b7a6d25e813
// last-edited: 2026-11-18

//go:build unix

package updater

// Fixtures of the core updater tests: an in-memory release source, a fake
// binary that answers --version-json, a recording Restarter and a rig that
// wires them to an Updater over a temp executable. Names are Gotham's: the
// app is "gotham", the binary "gotham-hq", the releases are Batman versions.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

const (
	testApp    = "gotham"
	testBinary = "gotham-hq"
	oldBinary  = "the running binary"
)

// fakeBinary is an executable that prints one --version-json line.
func fakeBinary(app, version string) []byte {
	return fmt.Appendf(nil, "#!/bin/sh\nprintf '%%s\\n' '{\"app\":\"%s\",\"version\":\"%s\",\"commit\":\"\",\"ui_build_id\":\"dev\",\"binary_id\":\"b1\"}'\n", app, version)
}

// scriptBinary is an executable with the given shell body.
func scriptBinary(body string) []byte { return []byte("#!/bin/sh\n" + body + "\n") }

// fakeSource is an in-memory release feed that records what was asked of it.
type fakeSource struct {
	mu      sync.Mutex
	latest  map[Channel]string // channel -> tag; absent = no release yet
	rels    map[string]Release // tag -> release
	bodies  map[string][]byte  // asset URL -> body
	sizes   map[string]int64   // asset URL -> advertised size override (absent = real length)
	latestN int
	opened  []string // asset names opened, in order
	openErr error
	midErr  map[string]error // asset URL -> error after half the body
	stall   map[string]bool  // asset URL -> half the body, then nothing until the context ends
	hang    map[string]bool  // asset URL -> no byte at all until the context ends
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		latest: map[Channel]string{}, rels: map[string]Release{},
		bodies: map[string][]byte{}, sizes: map[string]int64{}, midErr: map[string]error{}, stall: map[string]bool{}, hang: map[string]bool{},
	}
}

// assetNameFor is the raw asset name for this platform.
func assetNameFor() string { return RawBinary(testBinary)(Release{}, goos(), goarch()) }

// add publishes tag with body as its platform asset; stable marks it the
// stable channel's latest.
func (f *fakeSource) add(tag string, body []byte, stable bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	url := "mem://" + tag + "/" + assetNameFor()
	f.bodies[url] = body
	f.rels[tag] = Release{
		Version: tag[1:], Channel: ChannelStable, Notes: "notes for " + tag,
		URL:    "https://example.invalid/releases/" + tag,
		Assets: []Asset{{Name: assetNameFor(), URL: url, Size: int64(len(body))}},
	}
	if stable {
		f.latest[ChannelStable] = tag
	}
}

func (f *fakeSource) Latest(_ context.Context, ch Channel) (Release, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latestN++
	tag, ok := f.latest[ch]
	if !ok {
		return Release{}, ErrReleaseNotFound
	}
	return f.rels[tag], nil
}

func (f *fakeSource) Open(ctx context.Context, a Asset) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, a.Name)
	if f.openErr != nil {
		return nil, -1, f.openErr
	}
	body, ok := f.bodies[a.URL]
	if !ok {
		return nil, -1, errors.New("no such asset")
	}
	size := int64(len(body))
	if s, ok := f.sizes[a.URL]; ok {
		size = s
	}
	if err, ok := f.midErr[a.URL]; ok {
		return io.NopCloser(io.MultiReader(bytes.NewReader(body[:len(body)/2]), &errReader{err})), size, nil
	}
	if f.hang[a.URL] {
		return io.NopCloser(&ctxReader{ctx}), size, nil
	}
	if f.stall[a.URL] {
		return io.NopCloser(io.MultiReader(bytes.NewReader(body[:len(body)/2]), &ctxReader{ctx})), size, nil
	}
	return io.NopCloser(bytes.NewReader(body)), size, nil
}

// ctxReader blocks until its context ends, as a body whose peer went quiet
// does (net/http's body read returns the context's error the same way).
type ctxReader struct{ ctx context.Context }

func (c *ctxReader) Read([]byte) (int, error) {
	<-c.ctx.Done()
	return 0, context.Cause(c.ctx)
}

type errReader struct{ err error }

func (e *errReader) Read([]byte) (int, error) { return 0, e.err }

func (f *fakeSource) openedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.opened...)
}

func (f *fakeSource) latestCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.latestN
}

// tagSource is fakeSource with the TagSource method.
type tagSource struct{ *fakeSource }

func (t tagSource) Release(_ context.Context, version string) (Release, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tag := version
	if tag != "" && tag[0] != 'v' {
		tag = "v" + tag
	}
	r, ok := t.rels[tag]
	if !ok {
		return Release{}, ErrReleaseNotFound
	}
	return r, nil
}

// fakeRestarter records the restarts requested.
type fakeRestarter struct {
	mu      sync.Mutex
	reasons []string
	err     error
}

func (r *fakeRestarter) Restart(_ context.Context, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
	return r.err
}

func (r *fakeRestarter) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reasons...)
}

// rig is an Updater wired to a fake source and a temp executable.
type rig struct {
	t     *testing.T
	u     *Updater
	src   *fakeSource
	exe   string
	rst   *fakeRestarter
	state string
}

type rigConfig struct {
	settings func() Settings
	reader   ReaderCheck
	metrics  *Metrics
	noState  bool
	legacy   bool
	mode     Mode
	version  string
	dbPath   string
	asTag    bool // serve through tagSource
	source   Source
}

type rigOpt func(*rigConfig)

func withSettings(f func() Settings) rigOpt { return func(c *rigConfig) { c.settings = f } }
func withReader(r ReaderCheck) rigOpt       { return func(c *rigConfig) { c.reader = r } }
func withMetrics(m *Metrics) rigOpt         { return func(c *rigConfig) { c.metrics = m } }
func withoutState() rigOpt                  { return func(c *rigConfig) { c.noState = true } }
func withLegacySelfTestOff() rigOpt         { return func(c *rigConfig) { c.legacy = true } }
func withMode(m Mode) rigOpt                { return func(c *rigConfig) { c.mode = m } }
func withoutTagAPI() rigOpt                 { return func(c *rigConfig) { c.asTag = false } }
func withSource(s Source) rigOpt            { return func(c *rigConfig) { c.source = s } }

func newRig(t *testing.T, current string, src *fakeSource, opts ...rigOpt) *rig {
	t.Helper()
	cfg := rigConfig{mode: ModeCheck, asTag: true, version: current}
	for _, o := range opts {
		o(&cfg)
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, testBinary)
	if err := os.WriteFile(exe, []byte(oldBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	rst := &fakeRestarter{}
	var source Source = src
	if cfg.asTag {
		source = tagSource{src}
	}
	if cfg.source != nil {
		source = cfg.source
	}
	with := []Option{
		WithVersion(current),
		WithExecutableForTest(func() (string, error) { return exe, nil }),
		WithMetrics(cfg.metrics),
		WithDBPath(cfg.dbPath),
	}
	state := ""
	if !cfg.noState {
		state = t.TempDir()
		with = append(with, WithStateDir(state))
	}
	if cfg.settings != nil {
		with = append(with, WithSettings(cfg.settings))
	}
	if cfg.legacy {
		with = append(with, LegacySkipSelfTest())
	}
	u, err := New(Options{AppID: testApp, BinaryName: testBinary, Source: source, DefaultMode: cfg.mode},
		Deps{Restarter: rst, ReaderCheck: cfg.reader}, with...)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, u: u, src: src, exe: exe, rst: rst, state: state}
}

// rc is a task run handle over a cancelable context.
func (r *rig) rc(kind tasks.Kind) *tasks.RC {
	return taskstest.NewFakeRun(context.Background(), kind).RC()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("%s exists (stat err %v), want absent", filepath.Base(path), err)
	}
}

// assertUntouched: the running binary is as it was and nothing was left beside it.
func (r *rig) assertUntouched() {
	r.t.Helper()
	if got := readFile(r.t, r.exe); got != oldBinary {
		r.t.Errorf("<exe> = %q, want the running binary untouched", got)
	}
	assertAbsent(r.t, r.exe+".new")
	assertAbsent(r.t, r.exe+".prev")
	if got := r.rst.calls(); len(got) != 0 {
		r.t.Errorf("restarts requested = %v, want none", got)
	}
}

// stagingSource runs onOpen as an asset download starts, as an operator's
// storage reset racing the update would.
type stagingSource struct {
	Source
	onOpen func()
}

func (s stagingSource) Open(ctx context.Context, a Asset) (io.ReadCloser, int64, error) {
	s.onOpen()
	return s.Source.Open(ctx, a)
}
