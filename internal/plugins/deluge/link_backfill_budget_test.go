// file: internal/plugins/deluge/link_backfill_budget_test.go
// version: 1.1.0
// guid: 6b1e9d34-0c7a-4f52-a8d3-5e72c9b04f81
// last-edited: 2026-10-11

package deluge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// budgetBytes is distinct content of a distinct size per n, so no two
// budgetBytes files share a size (and none cross-seeds another).
func budgetBytes(n int) []byte { return bytes.Repeat([]byte{byte('a' + n%26)}, 100+n) }

// unmatchedTorrents lists n single-file torrents, ids linkTorrentID(1..n),
// that no library row matches: the held-or-unmatched shape that stays at the
// front of the window order on every run.
func unmatchedTorrents(t *testing.T, f *linkFixture, n int) []delugeclient.TorrentDetail {
	t.Helper()
	var out []delugeclient.TorrentDetail
	for i := 1; i <= n; i++ {
		out = append(out, f.torrent(t, i, filepath.Join(f.downloads, fmt.Sprintf("t%d", i)),
			tfile{"Example Book/01.mp3", budgetBytes(i)}))
	}
	return out
}

func hashedSet(hc *hashCounter, ts []delugeclient.TorrentDetail) string {
	var got []string
	for i, d := range ts {
		if hc.hashed(d1(d)) > 0 {
			got = append(got, fmt.Sprint(i+1))
		}
	}
	return strings.Join(got, ",")
}

func TestLinkHashBudget_Defaults(t *testing.T) {
	for _, tc := range []struct {
		name            string
		limit, explicit int
		want            int64
	}{
		{"limited run: limit x 50", 3, 0, 150},
		{"no limit: a fixed ceiling", 0, 0, 100000},
		{"explicit wins over limit", 3, 7, 7},
		{"explicit with no limit", 0, 7, 7},
		{"negative is unset", 2, -5, 100},
	} {
		if got := linkHashBudget(tc.limit, tc.explicit); got != tc.want {
			t.Errorf("%s: linkHashBudget(%d, %d) = %d, want %d", tc.name, tc.limit, tc.explicit, got, tc.want)
		}
	}
}

// The budget stops the hashing of a limited run that keeps meeting torrents
// with nothing to write, instead of growing the window across the listing.
func TestLinkBackfill_HashBudgetStopsHashing(t *testing.T) {
	f := newLinkFixture(t)
	ts := unmatchedTorrents(t, f, 10)
	_, client := newFakeDelugeClient(t, ts...)
	hc := &hashCounter{}

	res, err := runLink(t, &Plugin{client: client, store: newLinkMock(), linkHash: hc.hash}, `{"dry_run":false,"limit":1,"max_hashed_files":3}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := hashedSet(hc, ts); got != "1,2,3" {
		t.Errorf("hashed torrents %q, want 1,2,3", got)
	}
	if res.HashedFiles != 3 || res.HashBudget != 3 || res.DeferredByHashBudget != 7 || res.DeferredByLimit != 0 || res.Unmatched != 3 {
		t.Errorf("hashed_files=%d hash_budget=%d deferred_by_hash_budget=%d deferred_by_limit=%d unmatched=%d, want 3/3/7/0/3",
			res.HashedFiles, res.HashBudget, res.DeferredByHashBudget, res.DeferredByLimit, res.Unmatched)
	}
	if res.NextStartAfter != linkTorrentID(3) {
		t.Errorf("next_start_after = %q, want the last torrent examined, %q", res.NextStartAfter, linkTorrentID(3))
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "hash_budget:") {
		t.Errorf("notes %q carry no hash_budget note", res.Notes)
	}
}

// A second run, given the first's next_start_after, examines different
// torrents, so held or unmatched ones in front cannot starve the rest; the
// last torrent in hash order is reached and linked on the fourth run. Without
// start_after the same front torrents are examined again.
func TestLinkBackfill_HashBudgetRotationReachesLaterTorrents(t *testing.T) {
	f := newLinkFixture(t)
	ts := unmatchedTorrents(t, f, 6)
	last := f.torrent(t, 7, filepath.Join(f.downloads, "t7"), tfile{"Example Book/01.mp3", exampleAudio})
	ts = append(ts, last)
	store := newLinkMock(f.libraryRow("bf-7", "seven.mp3", exampleAudio))
	_, client := newFakeDelugeClient(t, ts...)

	run := func(params string) (linkBackfillResult, string) {
		hc := &hashCounter{}
		res, err := runLink(t, &Plugin{client: client, store: store, linkHash: hc.hash}, params)
		if err != nil {
			t.Fatalf("run %s: %v", params, err)
		}
		return res, hashedSet(hc, ts)
	}
	const base = `"dry_run":false,"limit":1,"max_hashed_files":2`

	res, got := run(`{` + base + `}`)
	if got != "1,2" || res.NextStartAfter != linkTorrentID(2) {
		t.Fatalf("run 1: hashed %q next_start_after %q, want 1,2 and torrent 2", got, res.NextStartAfter)
	}
	if _, again := run(`{` + base + `}`); again != "1,2" {
		t.Errorf("without start_after a rerun hashed %q, want the same front torrents 1,2", again)
	}
	res, got = run(`{` + base + `,"start_after":"` + res.NextStartAfter + `"}`)
	if got != "3,4" || res.NextStartAfter != linkTorrentID(4) {
		t.Fatalf("run 2: hashed %q next_start_after %q, want 3,4 and torrent 4", got, res.NextStartAfter)
	}
	res, got = run(`{` + base + `,"start_after":"` + res.NextStartAfter + `"}`)
	if got != "5,6" || res.NextStartAfter != linkTorrentID(6) {
		t.Fatalf("run 3: hashed %q next_start_after %q, want 5,6 and torrent 6", got, res.NextStartAfter)
	}
	if store.row("bf-7").DelugeHash != "" {
		t.Fatal("torrent 7 linked before any run reached it")
	}
	res, got = run(`{` + base + `,"start_after":"` + res.NextStartAfter + `"}`)
	if got != "7" || linkedCount(t, res) != 1 || store.row("bf-7").DelugeHash != linkTorrentID(7) {
		t.Errorf("run 4: hashed %q linked=%d bf-7=%q, want torrent 7 alone, linked", got, linkedCount(t, res), store.row("bf-7").DelugeHash)
	}
}

// start_after wraps round, and a run that examined every torrent no row
// carries yet reports no cursor and defers nothing.
func TestLinkBackfill_StartAfterWrapsAndExhausts(t *testing.T) {
	f := newLinkFixture(t)
	ts := unmatchedTorrents(t, f, 6)
	_, client := newFakeDelugeClient(t, ts...)

	hc := &hashCounter{}
	res, err := runLink(t, &Plugin{client: client, store: newLinkMock(), linkHash: hc.hash},
		`{"dry_run":false,"limit":1,"max_hashed_files":3,"start_after":"`+strings.ToUpper(linkTorrentID(4))+`"}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := hashedSet(hc, ts); got != "1,5,6" || res.NextStartAfter != linkTorrentID(1) {
		t.Errorf("hashed %q next_start_after %q, want 1,5,6 (5, 6, then wrapped to 1) and torrent 1", got, res.NextStartAfter)
	}

	hc = &hashCounter{}
	res, err = runLink(t, &Plugin{client: client, store: newLinkMock(), linkHash: hc.hash}, `{"dry_run":false,"limit":1,"max_hashed_files":6}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := hashedSet(hc, ts); got != "1,2,3,4,5,6" || res.NextStartAfter != "" || res.DeferredByHashBudget != 0 {
		t.Errorf("hashed %q next_start_after %q deferred_by_hash_budget %d, want all six, no cursor, no deferral",
			got, res.NextStartAfter, res.DeferredByHashBudget)
	}
}

func TestLinkBackfill_StartAfterMustBeATorrentHash(t *testing.T) {
	f := newLinkFixture(t)
	ts := unmatchedTorrents(t, f, 2)
	fd, client := newFakeDelugeClient(t, ts...)
	hc := &hashCounter{}
	_, err := runLink(t, &Plugin{client: client, store: newLinkMock(), linkHash: hc.hash}, `{"dry_run":false,"start_after":"not-a-hash"}`)
	if err == nil || !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("err = %v, want a refusal that says nothing was written", err)
	}
	if len(fd.methods()) != 0 || hashedSet(hc, ts) != "" {
		t.Errorf("deluge calls %v, hashed %q: a bad start_after must stop before any work", fd.methods(), hashedSet(hc, ts))
	}
}

// The budget counts the cross-seed check and never skips it: A (the first
// torrent) costs its own file plus C's, the cross-seed of its bytes, so B
// does not fit in a budget of 2. C is hashed, its row is ambiguous, and
// nothing is linked.
func TestLinkBackfill_HashBudgetChargesCrossSeedCheck(t *testing.T) {
	f := newLinkFixture(t)
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", exampleAudio})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Other Book/01.mp3", budgetBytes(2)})
	c := f.torrent(t, 3, filepath.Join(f.downloads, "c"), tfile{"Example Book/01.mp3", exampleAudio})
	store := newLinkMock(f.libraryRow("bf-1", "01.mp3", exampleAudio))
	_, client := newFakeDelugeClient(t, a, b, c)
	hc := &hashCounter{}

	res, err := runLink(t, &Plugin{client: client, store: store, linkHash: hc.hash}, `{"dry_run":false,"max_hashed_files":2}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if hc.hashed(d1(a)) != 1 || hc.hashed(d1(c)) != 1 || hc.hashed(d1(b)) != 0 {
		t.Errorf("hashed a=%d b=%d c=%d, want 1/0/1: the cross-seed is checked, the next torrent does not fit",
			hc.hashed(d1(a)), hc.hashed(d1(b)), hc.hashed(d1(c)))
	}
	if res.HashedFiles != 2 || res.BeyondLimitHashed != 1 || res.DeferredByHashBudget != 2 || res.Ambiguous != 1 || linkedCount(t, res) != 0 {
		t.Errorf("hashed_files=%d beyond_limit_hashed=%d deferred_by_hash_budget=%d ambiguous=%d linked=%d, want 2/1/2/1/0",
			res.HashedFiles, res.BeyondLimitHashed, res.DeferredByHashBudget, res.Ambiguous, linkedCount(t, res))
	}
	if store.row("bf-1").DelugeHash != "" {
		t.Error("a cross-seeded row was linked")
	}
}

// A budget below one torrent's size still examines the first torrent, or no
// run would ever progress; the next torrent, which does not fit, is deferred.
func TestLinkBackfill_HashBudgetAlwaysExaminesFirstTorrent(t *testing.T) {
	f := newLinkFixture(t)
	big := f.torrent(t, 1, filepath.Join(f.downloads, "big"),
		tfile{"Example Book/01.mp3", budgetBytes(1)}, tfile{"Example Book/02.mp3", budgetBytes(2)}, tfile{"Example Book/03.mp3", budgetBytes(3)})
	next := f.torrent(t, 2, filepath.Join(f.downloads, "next"), tfile{"Example Book/01.mp3", budgetBytes(4)})
	_, client := newFakeDelugeClient(t, big, next)
	hc := &hashCounter{}

	res, err := runLink(t, &Plugin{client: client, store: newLinkMock(), linkHash: hc.hash}, `{"dry_run":false,"max_hashed_files":2}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.HashedFiles != 3 || res.DeferredByHashBudget != 1 || hc.hashed(d1(next)) != 0 {
		t.Errorf("hashed_files=%d deferred_by_hash_budget=%d next hashed %d, want 3/1/0", res.HashedFiles, res.DeferredByHashBudget, hc.hashed(d1(next)))
	}
}

// Admission stops at the first torrent that does not fit: a cheaper torrent
// behind it is not skipped to, so the order (and next_start_after) stays
// the order.
func TestLinkBackfill_HashBudgetStopsAtFirstThatDoesNotFit(t *testing.T) {
	f := newLinkFixture(t)
	small := f.torrent(t, 1, filepath.Join(f.downloads, "s"), tfile{"Example Book/01.mp3", budgetBytes(1)})
	big := f.torrent(t, 2, filepath.Join(f.downloads, "b"),
		tfile{"Example Book/01.mp3", budgetBytes(2)}, tfile{"Example Book/02.mp3", budgetBytes(3)}, tfile{"Example Book/03.mp3", budgetBytes(4)})
	cheap := f.torrent(t, 3, filepath.Join(f.downloads, "c"), tfile{"Example Book/01.mp3", budgetBytes(5)})
	_, client := newFakeDelugeClient(t, small, big, cheap)
	hc := &hashCounter{}

	res, err := runLink(t, &Plugin{client: client, store: newLinkMock(), linkHash: hc.hash}, `{"dry_run":false,"max_hashed_files":3}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if hc.hashed(d1(small)) != 1 || hc.hashed(d1(big)) != 0 || hc.hashed(d1(cheap)) != 0 {
		t.Errorf("hashed small=%d big=%d cheap=%d, want 1/0/0", hc.hashed(d1(small)), hc.hashed(d1(big)), hc.hashed(d1(cheap)))
	}
	if res.DeferredByHashBudget != 2 || res.NextStartAfter != linkTorrentID(1) {
		t.Errorf("deferred_by_hash_budget=%d next_start_after=%q, want 2 and torrent 1", res.DeferredByHashBudget, res.NextStartAfter)
	}
}

// next_start_after names the last torrent examined only while a torrent no
// row carries yet is left: torrents some row already carries are not work the
// cursor is for.
func TestLinkBackfill_NextStartAfterIgnoresLinkedTorrents(t *testing.T) {
	f := newLinkFixture(t)
	ca := budgetBytes(1)
	linked := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", ca})
	pending := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/01.mp3", budgetBytes(2)})
	row := f.libraryRow("bf-a", "a.mp3", ca)
	row.DelugeHash = linkTorrentID(1)
	_, client := newFakeDelugeClient(t, linked, pending)

	res, err := runLink(t, &Plugin{client: client, store: newLinkMock(row)}, `{"dry_run":false,"limit":1,"max_hashed_files":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.DeferredByHashBudget != 1 || res.NextStartAfter != "" {
		t.Errorf("deferred_by_hash_budget=%d next_start_after=%q, want 1 and no cursor: only the linked torrent is left", res.DeferredByHashBudget, res.NextStartAfter)
	}
}

// The limit, not the budget, is the reason when the limit is what stopped the
// run, and the budget defaults from it.
func TestLinkBackfill_LimitStillNamesItsOwnDeferral(t *testing.T) {
	f := newLinkFixture(t)
	ca, cb := budgetBytes(1), budgetBytes(2)
	a := f.torrent(t, 1, filepath.Join(f.downloads, "a"), tfile{"Example Book/01.mp3", ca})
	b := f.torrent(t, 2, filepath.Join(f.downloads, "b"), tfile{"Example Book/01.mp3", cb})
	store := newLinkMock(f.libraryRow("bf-a", "a.mp3", ca), f.libraryRow("bf-b", "b.mp3", cb))
	_, client := newFakeDelugeClient(t, a, b)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"limit":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.DeferredByLimit != 1 || res.DeferredByHashBudget != 0 || res.HashBudget != 50 {
		t.Errorf("deferred_by_limit=%d deferred_by_hash_budget=%d hash_budget=%d, want 1/0/50", res.DeferredByLimit, res.DeferredByHashBudget, res.HashBudget)
	}
	if res.NextStartAfter != linkTorrentID(1) {
		t.Errorf("next_start_after = %q, want torrent 1 (torrent 2 is still unexamined)", res.NextStartAfter)
	}
}

// matchedTorrents lists n single-file torrents, ids linkTorrentID(1..n), each
// matching one library row (bf-1..bf-n) by content, and returns the rows.
func matchedTorrents(t *testing.T, f *linkFixture, n int) ([]delugeclient.TorrentDetail, []database.BookFile) {
	t.Helper()
	var ts []delugeclient.TorrentDetail
	var rows []database.BookFile
	for i := 1; i <= n; i++ {
		ts = append(ts, f.torrent(t, i, filepath.Join(f.downloads, fmt.Sprintf("t%d", i)),
			tfile{"Example Book/01.mp3", budgetBytes(i)}))
		rows = append(rows, f.libraryRow(fmt.Sprintf("bf-%d", i), fmt.Sprintf("%02d.mp3", i), budgetBytes(i)))
	}
	return ts, rows
}

// fakeLinkClock is a clock each hash advances by step, so a test drives the
// deadline stop without sleeping.
type fakeLinkClock struct {
	now  atomic.Int64 // unix nanos
	step time.Duration
}

func (c *fakeLinkClock) Now() time.Time { return time.Unix(0, c.now.Load()) }

func (c *fakeLinkClock) hash(path string) (string, error) {
	c.now.Add(int64(c.step))
	return filehash.BookFileHash(path)
}

func runLinkCtx(t *testing.T, ctx context.Context, p *Plugin, params string) (linkBackfillResult, error) {
	t.Helper()
	r := &linkTestReporter{}
	err := p.runLinkBackfill(ctx, json.RawMessage(params), r)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.result == nil {
		return linkBackfillResult{}, err
	}
	return r.result.(linkBackfillResult), err
}

// A run with no limit whose listing would outlast the op's timeout stops
// admitting torrents before the deadline, still writes the links it found,
// and persists next_start_after, so the next run continues instead of
// hashing the same torrents again. Until 2026-10-11 such a run hashed the
// whole listing in one round and wrote only after it: on the timeout it wrote
// nothing and reported no cursor.
//
// The clock is the plugin's: each hash advances it 10 minutes, the deadline
// is an hour away, so the reserve is 15 minutes (a quarter of the hour, at
// the cap) and admitting stops once the clock plus the last round's length
// reaches 45 minutes. One torrent file per round: torrents 1-4 are examined
// and linked, 5 and 6 are deferred_by_deadline.
func TestLinkBackfill_DeadlineStopsAdmittingAndWrites(t *testing.T) {
	f := newLinkFixture(t)
	ts, rows := matchedTorrents(t, f, 6)
	store := newLinkMock(rows...)
	_, client := newFakeDelugeClient(t, ts...)

	run := func(params string) linkBackfillResult {
		t.Helper()
		start := time.Now()
		clock := &fakeLinkClock{step: 10 * time.Minute}
		clock.now.Store(start.UnixNano())
		ctx, cancel := context.WithDeadline(context.Background(), start.Add(time.Hour))
		defer cancel()
		res, err := runLinkCtx(t, ctx, &Plugin{client: client, store: store, linkHash: clock.hash, linkNow: clock.Now, linkRoundFiles: 1}, params)
		if err != nil {
			t.Fatalf("run %s: %v", params, err)
		}
		return res
	}

	res := run(`{"dry_run":false}`)
	if got := linkedCount(t, res); got != 4 {
		t.Errorf("linked = %d, want 4", got)
	}
	if res.DeferredByDeadline != 2 || res.DeferredByHashBudget != 0 || res.DeferredByLimit != 0 {
		t.Errorf("deferred_by_deadline=%d deferred_by_hash_budget=%d deferred_by_limit=%d, want 2/0/0",
			res.DeferredByDeadline, res.DeferredByHashBudget, res.DeferredByLimit)
	}
	if res.NextStartAfter != linkTorrentID(4) {
		t.Errorf("next_start_after = %q, want the last torrent examined, %q", res.NextStartAfter, linkTorrentID(4))
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "deadline:") {
		t.Errorf("notes %q carry no deadline note", res.Notes)
	}
	for i := 1; i <= 6; i++ {
		got := store.row(fmt.Sprintf("bf-%d", i)).DelugeHash
		if want := linkTorrentID(i); (i <= 4) != (got == want) {
			t.Errorf("bf-%d DelugeHash = %q after run 1 (torrent %d examined: %v)", i, got, i, i <= 4)
		}
	}

	// The next run, from the cursor, reaches the rest.
	res = run(`{"dry_run":false,"start_after":"` + res.NextStartAfter + `"}`)
	if got := linkedCount(t, res); got != 2 {
		t.Errorf("run 2 linked = %d, want 2", got)
	}
	for i := 5; i <= 6; i++ {
		if got := store.row(fmt.Sprintf("bf-%d", i)).DelugeHash; got != linkTorrentID(i) {
			t.Errorf("run 2: bf-%d DelugeHash = %q, want %q", i, got, linkTorrentID(i))
		}
	}
}

// With no deadline (or one far off) a run with no limit still examines the
// whole listing across its rounds: the round allowance splits the hashing,
// it does not cap it.
func TestLinkBackfill_RoundsCoverWholeListing(t *testing.T) {
	f := newLinkFixture(t)
	ts, rows := matchedTorrents(t, f, 5)
	store := newLinkMock(rows...)
	_, client := newFakeDelugeClient(t, ts...)
	hc := &hashCounter{}

	res, err := runLink(t, &Plugin{client: client, store: store, linkHash: hc.hash, linkRoundFiles: 2}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 5 || res.DeferredByDeadline != 0 || res.DeferredByHashBudget != 0 || res.NextStartAfter != "" {
		t.Errorf("linked=%d deferred_by_deadline=%d deferred_by_hash_budget=%d next_start_after=%q, want 5/0/0/\"\"",
			got, res.DeferredByDeadline, res.DeferredByHashBudget, res.NextStartAfter)
	}
	if got := hashedSet(hc, ts); got != "1,2,3,4,5" {
		t.Errorf("hashed %q, want every torrent once", got)
	}
}

// When the plan meets limit in the round that also spends the hash budget,
// the torrents left are the limit's: they would have been left anyway.
func TestLinkBackfill_LimitAndBudgetTogetherDeferByLimit(t *testing.T) {
	f := newLinkFixture(t)
	ts, rows := matchedTorrents(t, f, 4)
	store := newLinkMock(rows...)
	_, client := newFakeDelugeClient(t, ts...)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false,"limit":1,"max_hashed_files":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := linkedCount(t, res); got != 1 || res.DeferredByLimit != 3 || res.DeferredByHashBudget != 0 {
		t.Errorf("linked=%d deferred_by_limit=%d deferred_by_hash_budget=%d, want 1/3/0",
			got, res.DeferredByLimit, res.DeferredByHashBudget)
	}
}

// A label Deluge lists no torrent under ends the run before the full-library
// book_file scan, and still reports why it did nothing.
func TestLinkBackfill_NoTorrentsSkipsLibraryScan(t *testing.T) {
	newLinkFixture(t)
	store := newLinkMock()
	store.GetAllBookFilesCoreCompleteFunc = func() ([]database.BookFileCore, error) {
		t.Error("the full-library scan ran for a label with no torrents")
		return nil, nil
	}
	_, client := newFakeDelugeClient(t)

	res, err := runLink(t, &Plugin{client: client, store: store}, `{"dry_run":false}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Torrents != 0 || !strings.Contains(strings.Join(res.Notes, "\n"), "no_torrents:") {
		t.Errorf("torrents=%d notes=%q, want 0 and a no_torrents note", res.Torrents, res.Notes)
	}
}
