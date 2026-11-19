// file: internal/plugins/deluge/kinds_link_test.go
// version: 1.0.0
// guid: 8e41b7c2-d5a9-4f13-b0c7-62a9e3d1f584
// last-edited: 2026-11-19

package deluge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/tasks"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// linkScenario is one deluge.link-backfill case: the torrents on disk, the
// library rows, and the params both shells receive (the mode is separate).
type linkScenario struct {
	name     string
	torrents func(t *testing.T, fx *linkFixture) []delugeclient.TorrentDetail
	rows     func(fx *linkFixture) []database.BookFile
	params   string // without dry_run/dryRun: those are the mode
	live     bool
}

func oneToOne(t *testing.T, fx *linkFixture) []delugeclient.TorrentDetail {
	return []delugeclient.TorrentDetail{fx.torrent(t, 1, fx.downloads, tfile{rel: "Year One/01.mp3", content: exampleAudio})}
}

func oneRow(fx *linkFixture) []database.BookFile {
	return []database.BookFile{fx.libraryRow("bf-year-one", "01.mp3", exampleAudio)}
}

func threeTorrents(t *testing.T, fx *linkFixture) ([]delugeclient.TorrentDetail, []database.BookFile) {
	var tors []delugeclient.TorrentDetail
	var rows []database.BookFile
	for i := 1; i <= 3; i++ {
		c := fmt.Appendf(nil, "bytes of Batman issue %d", i)
		tors = append(tors, fx.torrent(t, i, filepath.Join(fx.downloads, fmt.Sprintf("t%d", i)), tfile{rel: "Issue/01.mp3", content: c}))
		rows = append(rows, fx.libraryRow(fmt.Sprintf("bf-issue-%d", i), fmt.Sprintf("%d.mp3", i), c))
	}
	return tors, rows
}

func linkJSON(t *testing.T, res linkBackfillResult) string {
	t.Helper()
	b, err := json.Marshal(res)
	require.NoError(t, err)
	return string(b)
}

func headLinkParams(params string, live bool) string {
	mode := `"dry_run":true`
	if live {
		mode = `"dry_run":false`
	}
	if params == "" {
		return "{" + mode + "}"
	}
	return "{" + mode + "," + params[1:]
}

func TestDelugeCores_LinkBackfillParity(t *testing.T) {
	scenarios := []linkScenario{
		{name: "preview plans the link and writes nothing", torrents: oneToOne, rows: oneRow},
		{name: "live links the verified row", torrents: oneToOne, rows: oneRow, live: true},
		{name: "unverified content is not linked", live: true, rows: func(fx *linkFixture) []database.BookFile {
			return []database.BookFile{fx.libraryRow("bf-year-one", "01.mp3", []byte("some other bytes entirely"))}
		}, torrents: oneToOne},
		{name: "a torrent no row matches", live: true, torrents: oneToOne, rows: func(*linkFixture) []database.BookFile { return nil }},
		{name: "an already linked row is left alone", live: true, torrents: oneToOne, rows: func(fx *linkFixture) []database.BookFile {
			r := oneRow(fx)
			r[0].DelugeHash = linkTorrentID(1)
			return r
		}},
		{name: "limit caps the torrents written", live: true, params: `{"limit":1}`,
			torrents: func(t *testing.T, fx *linkFixture) []delugeclient.TorrentDetail {
				tors, _ := threeTorrents(t, fx)
				return tors
			},
			rows: func(fx *linkFixture) []database.BookFile {
				var rows []database.BookFile
				for i := 1; i <= 3; i++ {
					c := fmt.Appendf(nil, "bytes of Batman issue %d", i)
					rows = append(rows, fx.libraryRow(fmt.Sprintf("bf-issue-%d", i), fmt.Sprintf("%d.mp3", i), c))
				}
				return rows
			}},
		{name: "workers and a hash budget are taken from the params", live: true, params: `{"workers":2,"max_hashed_files":1}`,
			torrents: func(t *testing.T, fx *linkFixture) []delugeclient.TorrentDetail {
				tors, _ := threeTorrents(t, fx)
				return tors
			},
			rows: func(fx *linkFixture) []database.BookFile {
				var rows []database.BookFile
				for i := 1; i <= 3; i++ {
					c := fmt.Appendf(nil, "bytes of Batman issue %d", i)
					rows = append(rows, fx.libraryRow(fmt.Sprintf("bf-issue-%d", i), fmt.Sprintf("%d.mp3", i), c))
				}
				return rows
			}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			fx := newLinkFixture(t) // sets the globals HEAD reads
			tors := sc.torrents(t, fx)
			headStore := newLinkMock(sc.rows(fx)...)
			natStore := newLinkMock(sc.rows(fx)...)

			// HEAD.
			_, client := newFakeDelugeClient(t, tors...)
			headRes, headErr := runLink(t, &Plugin{client: client, store: headStore}, headLinkParams(sc.params, sc.live))

			// Native: own config, poisoned globals.
			poisonGlobals(t)
			w := &recWriter{modify: natStore.ModifyBookFile}
			deps := KindDeps{
				Store: guardedStoreOf(t, natStore.MockStore), NewWriter: w.factory, Client: &fakeClient{torrents: tors},
				Config: &config.Config{RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel},
			}
			mode := tasks.ModePreview
			if sc.live {
				mode = tasks.ModeLive
			}
			def := byKind(t, (&Plugin{}).Kinds(deps, nil), linkBackfillKind)
			params := sc.params
			_, out, natErr := runKindResult(t, def, mode, &sentinelWriter{}, params)

			require.Equal(t, errText(headErr), errText(natErr))
			require.NoError(t, natErr)
			var natRes linkBackfillResult
			require.NoError(t, json.Unmarshal(out, &natRes))
			require.Equal(t, linkJSON(t, headRes), linkJSON(t, natRes), "result of the two shells")
			require.Equal(t, !sc.live, natRes.DryRun)
			for _, r := range sc.rows(fx) {
				require.Equal(t, headStore.row(r.ID), natStore.row(r.ID), "row %s", r.ID)
			}
			require.Equal(t, headStore.modifyCalls.Load(), natStore.modifyCalls.Load(), "writes")
			if !sc.live {
				require.Zero(t, natStore.modifyCalls.Load(), "a Preview run writes nothing")
				require.Empty(t, w.writers(), "a Preview run builds no writer")
			}
		})
	}
}

// The Deluge listing failing is the run's error in both shells; a nil Client is
// refused in both; neither writes.
func TestDelugeCores_LinkBackfillDelugeUnavailable(t *testing.T) {
	fx := newLinkFixture(t)
	rows := oneRow(fx)

	hs := newLinkMock(rows...)
	_, herr := runLink(t, &Plugin{store: hs}, `{"dry_run":false}`)
	require.Error(t, herr, "HEAD: nil client")

	poisonGlobals(t)
	for name, cl := range map[string]delugeClient{
		"nil client":   nil,
		"list failure": &fakeClient{listErr: errors.New("daemon unreachable")},
	} {
		t.Run(name, func(t *testing.T) {
			ns := newLinkMock(rows...)
			w := &recWriter{modify: ns.ModifyBookFile}
			deps := KindDeps{Store: guardedStoreOf(t, ns.MockStore), NewWriter: w.factory, Client: cl,
				Config: &config.Config{RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel}}
			def := byKind(t, (&Plugin{}).Kinds(deps, nil), linkBackfillKind)
			_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
			require.Error(t, err)
			require.Zero(t, ns.modifyCalls.Load())
		})
	}
}

// The label param is trimmed and reaches the listing; an empty one falls back to
// the configured discovery label.
func TestDelugeCores_LinkBackfillLabel(t *testing.T) {
	fx := newLinkFixture(t)
	poisonGlobals(t)
	for _, tc := range []struct{ params, want string }{
		{`{"label":" example-custom "}`, "example-custom"},
		{`{}`, linkTestLabel},
	} {
		fc := &fakeClient{}
		ns := newLinkMock()
		deps := KindDeps{Store: guardedStoreOf(t, ns.MockStore), NewWriter: (&recWriter{}).factory, Client: fc,
			Config: &config.Config{RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel}}
		def := byKind(t, (&Plugin{}).Kinds(deps, nil), linkBackfillKind)
		_, out, err := runKindResult(t, def, tasks.ModePreview, &sentinelWriter{}, tc.params)
		require.NoError(t, err)
		require.Equal(t, []string{tc.want}, fc.labels)
		var res linkBackfillResult
		require.NoError(t, json.Unmarshal(out, &res))
		require.Equal(t, tc.want, res.Label)
	}
}

// A Live run with a store that cannot write is a refusal before any work, not a
// nil writer reached mid-run.
func TestDelugeCores_LinkBackfillLiveNeedsWriter(t *testing.T) {
	fx := newLinkFixture(t)
	tors := oneToOne(t, fx)
	ns := newLinkMock(oneRow(fx)...)
	poisonGlobals(t)
	deps := KindDeps{Store: guardedStoreOf(t, ns.MockStore), Client: &fakeClient{torrents: tors},
		Config: &config.Config{RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel}}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), linkBackfillKind)
	_, err := runKind(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.ErrorContains(t, err, "KindDeps.NewWriter is not set")
	require.Zero(t, ns.modifyCalls.Load())
}

// A write-capable core that is handed a nil writer in a live env refuses rather
// than dereferencing it (the core's own guard, reachable by a shell that forgot).
func TestDelugeCores_LinkBackfillCoreRefusesNilWriter(t *testing.T) {
	fx := newLinkFixture(t)
	tors := oneToOne(t, fx)
	ns := newLinkMock(oneRow(fx)...)
	env := linkBackfillEnv{
		kindEnv: headEnv(&parityReporter{}, true),
		Client:  &fakeClient{torrents: tors}, Store: guardedStoreOf(t, ns.MockStore),
		DefaultLabel: linkTestLabel, Root: fx.root,
	}
	_, err := linkBackfillCore(context.Background(), linkBackfillCoreParams{}, env)
	require.Error(t, err)
	require.Zero(t, ns.modifyCalls.Load())
}

// HEAD's mode switch for link-backfill: disagreeing spellings preview and say so.
func TestDelugeCores_LinkBackfillHEADModeDisagree(t *testing.T) {
	fx := newLinkFixture(t)
	tors := oneToOne(t, fx)
	hs := newLinkMock(oneRow(fx)...)
	_, client := newFakeDelugeClient(t, tors...)
	res, err := runLink(t, &Plugin{client: client, store: hs}, `{"dry_run":true,"dryRun":false}`)
	require.NoError(t, err)
	require.True(t, res.DryRun)
	require.Zero(t, hs.modifyCalls.Load())
	require.NotEmpty(t, res.Notes)
}

// provGuarded keeps a native test store's FileProvenanceStore capability (a
// guardedStore hides it): the provenance rule needs it through KindDeps.Store.
type provGuarded struct {
	guardedStore
	database.FileProvenanceStore
}

// The native kind resolves the provenance capability off KindDeps.Store, as the
// HEAD shell does off p.store: with it, a row whose bytes only the provenance
// chain recorded links; without it the run counts provenance_unavailable.
func TestDelugeCores_LinkBackfillNativeProvenanceRule(t *testing.T) {
	build := func(t *testing.T, fx *linkFixture) (*provLinkMock, []delugeclient.TorrentDetail) {
		tor := fx.torrent(t, 1, fx.downloads, tfile{rel: "Year One/01.mp3", content: exampleAudio})
		h := sha256Hex(exampleAudio)
		row := fx.libraryRow("bf-1", "renamed-01.mp3", exampleAudio)
		row.FileHash = sha256Hex([]byte("tagged bytes"))
		row.FileSize += 100
		return &provLinkMock{
			linkMock: newLinkMock(row),
			eventsByHash: map[string][]database.FileEvent{
				h: {{BookFileID: "bf-1", Path: row.FilePath, Digest: database.FileDigest{SHA256Full: h, SizeBytes: int64(len(exampleAudio))}}},
			},
			history: map[string][]database.FileEvent{
				"bf-1": {
					{BookFileID: "bf-1", Path: row.FilePath, Digest: database.FileDigest{SHA256Full: h, SizeBytes: int64(len(exampleAudio))}},
					{BookFileID: "bf-1", Path: row.FilePath, Digest: database.FileDigest{SHA256Full: row.FileHash, SizeBytes: row.FileSize}},
				},
			},
		}, []delugeclient.TorrentDetail{tor}
	}
	run := func(t *testing.T, withProv bool) (linkBackfillResult, *provLinkMock) {
		fx := newLinkFixture(t)
		ps, tors := build(t, fx)
		poisonGlobals(t)
		var store kindStore = guardedStoreOf(t, ps.MockStore)
		if withProv {
			store = provGuarded{guardedStore: guardedStoreOf(t, ps.MockStore), FileProvenanceStore: ps}
		}
		w := &recWriter{modify: ps.linkMock.ModifyBookFile}
		deps := KindDeps{Store: store, NewWriter: w.factory, Client: &fakeClient{torrents: tors},
			Config: &config.Config{RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel}}
		def := byKind(t, (&Plugin{}).Kinds(deps, nil), linkBackfillKind)
		_, out, err := runKindResult(t, def, tasks.ModeLive, &sentinelWriter{}, "")
		require.NoError(t, err)
		var res linkBackfillResult
		require.NoError(t, json.Unmarshal(out, &res))
		return res, ps
	}
	res, ps := run(t, true)
	require.Zero(t, res.ProvenanceUnavailable)
	require.Equal(t, linkTorrentID(1), ps.row("bf-1").DelugeHash, "the provenance rule linked the row")

	res, ps = run(t, false)
	require.NotZero(t, res.ProvenanceUnavailable)
	require.Empty(t, ps.row("bf-1").DelugeHash, "without the capability the row is not linked")
}

// The injected hash, clock and round size reach the native core (Plugin.linkHash,
// linkNow, linkRoundFiles at HEAD).
func TestDelugeCores_LinkBackfillNativeSeamsReachCore(t *testing.T) {
	fx := newLinkFixture(t)
	tors := oneToOne(t, fx)
	ns := newLinkMock(oneRow(fx)...)
	poisonGlobals(t)
	hc := &hashCounter{}
	var nowCalls int64
	var mu sync.Mutex
	deps := KindDeps{
		Store: guardedStoreOf(t, ns.MockStore), NewWriter: (&recWriter{modify: ns.ModifyBookFile}).factory,
		Client: &fakeClient{torrents: tors}, Config: &config.Config{RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel},
		LinkHash: hc.hash,
		LinkNow:  func() time.Time { mu.Lock(); nowCalls++; mu.Unlock(); return time.Now() },
	}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), linkBackfillKind)
	_, _, err := runKindResult(t, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	require.Equal(t, 1, hc.hashed(d1(tors[0])), "the injected hasher hashed the torrent file")
	mu.Lock()
	defer mu.Unlock()
	require.NotZero(t, nowCalls, "the injected clock was read")
}

// The native run honours the injected clock and the round allowance (KindDeps
// LinkNow, LinkHash, LinkRoundFiles): with one torrent file per round and each
// hash advancing the clock 10 minutes under a one-hour deadline, torrents 1-4 are
// linked and 5-6 are deferred by the deadline, as HEAD's
// TestLinkBackfill_DeadlineStopsAdmittingAndWrites does through the plugin fields.
func TestDelugeCores_LinkBackfillNativeDeadlineSeams(t *testing.T) {
	fx := newLinkFixture(t)
	tors, rows := matchedTorrents(t, fx, 6)
	ns := newLinkMock(rows...)
	poisonGlobals(t)
	start := time.Now()
	clock := &fakeLinkClock{step: 10 * time.Minute}
	clock.now.Store(start.UnixNano())
	deps := KindDeps{
		Store: guardedStoreOf(t, ns.MockStore), NewWriter: (&recWriter{modify: ns.ModifyBookFile}).factory,
		Client: &fakeClient{torrents: tors}, Config: &config.Config{RootDir: fx.root, DelugeDiscoveryLabel: linkTestLabel},
		LinkHash: clock.hash, LinkNow: clock.Now, LinkRoundFiles: 1,
	}
	def := byKind(t, (&Plugin{}).Kinds(deps, nil), linkBackfillKind)
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(time.Hour))
	defer cancel()
	_, out, err := runKindCtx(t, ctx, def, tasks.ModeLive, &sentinelWriter{}, "")
	require.NoError(t, err)
	var res linkBackfillResult
	require.NoError(t, json.Unmarshal(out, &res))
	require.EqualValues(t, 4, linkedCount(t, res))
	require.EqualValues(t, 2, res.DeferredByDeadline)
	require.Equal(t, linkTorrentID(4), res.NextStartAfter)
}

// The core refuses a missing Client, Store or Root before it lists or writes
// anything, each with its own message (a dry run needs no writer).
func TestDelugeCores_LinkBackfillCoreRefusesMissingDeps(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(env *linkBackfillEnv, fx *linkFixture, ns *linkMock)
		want   string
	}{
		{"no client", func(env *linkBackfillEnv, _ *linkFixture, _ *linkMock) { env.Client = nil }, "deluge is not configured"},
		{"no store", func(env *linkBackfillEnv, _ *linkFixture, _ *linkMock) { env.Store = nil }, "store not available"},
		{"no root", func(env *linkBackfillEnv, _ *linkFixture, _ *linkMock) { env.Root = "" }, "root_dir is not set"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLinkFixture(t)
			fc := &fakeClient{torrents: oneToOne(t, fx)}
			ns := newLinkMock(oneRow(fx)...)
			env := linkBackfillEnv{
				kindEnv: headEnv(&parityReporter{}, false),
				Client:  fc, Store: guardedStoreOf(t, ns.MockStore),
				DefaultLabel: linkTestLabel, Root: fx.root,
			}
			tc.mutate(&env, fx, ns)
			_, err := linkBackfillCore(context.Background(), linkBackfillCoreParams{}, env)
			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, "nothing was written")
			require.Empty(t, fc.labels, "nothing was listed")
		})
	}
}

// A negative limit is no limit, and the start line says so (limit=0): the value
// the core logs is the one it works with.
func TestDelugeCores_LinkBackfillNegativeLimitIsNoLimit(t *testing.T) {
	fx := newLinkFixture(t)
	tors, rows := threeTorrents(t, fx)
	run := func(params string) (string, *parityReporter) {
		_, client := newFakeDelugeClient(t, tors...)
		rep := &parityReporter{}
		err := (&Plugin{client: client, store: newLinkMock(rows...)}).runLinkBackfill(context.Background(), json.RawMessage(params), rep)
		require.NoError(t, err)
		b, merr := json.Marshal(rep.result)
		require.NoError(t, merr)
		return string(b), rep
	}
	unlimited, _ := run(`{"dry_run":true}`)
	negative, rep := run(`{"dry_run":true,"limit":-1}`)
	require.Equal(t, unlimited, negative)
	require.Contains(t, rep.logged(), " limit=0 ", "the start line logs the clamped limit")
	require.NotContains(t, rep.logged(), " limit=-1 ")
}
