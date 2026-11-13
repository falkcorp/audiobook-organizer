// file: core/log/logtest/journal_test.go
// version: 1.0.0
// guid: c4e9a7d2-6f18-4b53-8e0a-2d5b7c9f1e64
// last-edited: 2026-11-13

package logtest

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// refStore is a reference journal: an in-memory store with the whole contract,
// plus one switch per way an implementation can break it. The suite passes on
// the clean store (it is implementable without core's native store) and fails,
// under the case that names the rule, on each broken one.
type refStore struct {
	mu    sync.Mutex
	items []refItem
	seq   uint64
	bug   refBug
}

type refItem struct {
	rec log.Record
	key string // time, then the tie-break: append order, or the source ID for an import
	src string // an import's dedup identity; "" for a native record
}

type refBug string

const (
	bugNone             refBug = ""
	bugOldestFirst      refBug = "oldest-first"
	bugNoDedup          refBug = "import-not-idempotent"
	bugDedupByTimeOnly  refBug = "import-key-ignores-source-id"
	bugImportsLast      refBug = "imports-ignore-their-time"
	bugPruneNewest      refBug = "prune-newest-first"
	bugPruneUnbounded   refBug = "prune-ignores-limit"
	bugPruneInclusive   refBug = "prune-before-inclusive"
	bugPruneNoMore      refBug = "prune-never-more"
	bugDropAttrs        refBug = "attrs-dropped"
	bugTextCaseExact    refBug = "text-case-sensitive"
	bugSinceExclusive   refBug = "since-exclusive"
	bugUntilInclusive   refBug = "until-inclusive"
	bugIgnoreLevel      refBug = "min-level-ignored"
	bugIgnoreModule     refBug = "module-ignored"
	bugCursorRepeats    refBug = "cursor-repeats-last"
	bugCursorSkips      refBug = "cursor-skips-one"
	bugIgnoreContext    refBug = "context-ignored"
	bugEqualTimeForward refBug = "equal-time-in-append-order"
	bugTimeToMillis     refBug = "time-truncated-to-millis"
	bugNextAlwaysSet    refBug = "next-set-on-last-page"
	bugFilterAfterLimit refBug = "filter-applied-after-limit"
)

func (s *refStore) ctxErr(ctx context.Context) error {
	if s.bug == bugIgnoreContext {
		return nil
	}
	return ctx.Err()
}

func timeKey(t time.Time) string { return fmt.Sprintf("%020d", t.UnixNano()) }

func (s *refStore) Append(ctx context.Context, recs []log.Record) error {
	if err := s.ctxErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range recs {
		s.seq++
		if s.bug == bugDropAttrs {
			r.Attrs = nil
		}
		r.Attrs = flatten("", r.Attrs)
		if s.bug == bugTimeToMillis {
			r.Time = r.Time.Truncate(time.Millisecond)
		}
		s.items = append(s.items, refItem{rec: r, key: timeKey(r.Time) + fmt.Sprintf("-n%020d", s.seq)})
	}
	return nil
}

func flatten(prefix string, attrs []slog.Attr) []slog.Attr {
	var out []slog.Attr
	for _, a := range attrs {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindGroup {
			p := a.Key
			if prefix != "" {
				p = prefix + "." + a.Key
			}
			out = append(out, flatten(p, v.Group())...)
			continue
		}
		k := a.Key
		if prefix != "" {
			k = prefix + "." + k
		}
		out = append(out, slog.Any(k, v.Any()))
	}
	return out
}

func (s *refStore) Import(ctx context.Context, recs []log.ImportedRecord) error {
	if err := s.ctxErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ir := range recs {
		id := ir.SourceID
		if s.bug == bugDedupByTimeOnly {
			id = ""
		}
		tie := "-i" + hashHex(id)
		if s.bug == bugNoDedup {
			s.seq++
			tie = fmt.Sprintf("-i%020d", s.seq)
		}
		key := timeKey(ir.Time) + tie
		if s.bug == bugImportsLast {
			s.seq++
			key = timeKey(time.Unix(0, 1<<62)) + fmt.Sprintf("-i%020d", s.seq)
		}
		if s.bug != bugNoDedup && slices.ContainsFunc(s.items, func(it refItem) bool { return it.key == key }) {
			continue
		}
		r := ir.Record
		r.Attrs = flatten("", r.Attrs)
		s.items = append(s.items, refItem{rec: r, key: key, src: ir.SourceID})
	}
	return nil
}

func hashHex(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%016x", h.Sum64())
}

// order returns the items oldest first by key (or by the broken rule).
func (s *refStore) order() []refItem {
	out := slices.Clone(s.items)
	slices.SortStableFunc(out, func(a, b refItem) int { return strings.Compare(a.key, b.key) })
	return out
}

const refDefaultLimit = 50

func (s *refStore) Query(ctx context.Context, q log.JournalQuery) (log.JournalPage, error) {
	if err := s.ctxErr(ctx); err != nil {
		return log.JournalPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ord := s.order()
	if s.bug == bugEqualTimeForward {
		// Break ties by append order instead of reverse append order: sort
		// natives of one instant ascending by sequence while still newest-first
		// by time (the order below reverses the whole list).
		slices.Reverse(ord)
		slices.SortStableFunc(ord, func(a, b refItem) int {
			if c := strings.Compare(timeKey(b.rec.Time), timeKey(a.rec.Time)); c != 0 {
				return c
			}
			return strings.Compare(a.key, b.key)
		})
		slices.Reverse(ord)
	}
	if s.bug != bugOldestFirst {
		slices.Reverse(ord)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = refDefaultLimit
	}
	match := func(r log.Record) bool {
		if s.bug != bugIgnoreLevel && r.Level < q.MinLevel {
			return false
		}
		if s.bug != bugIgnoreModule && q.Module != "" && r.Module != q.Module {
			return false
		}
		if !q.Since.IsZero() {
			if s.bug == bugSinceExclusive && !r.Time.After(q.Since) || s.bug != bugSinceExclusive && r.Time.Before(q.Since) {
				return false
			}
		}
		if !q.Until.IsZero() {
			if s.bug == bugUntilInclusive && r.Time.After(q.Until) || s.bug != bugUntilInclusive && !r.Time.Before(q.Until) {
				return false
			}
		}
		if q.Text != "" {
			if s.bug == bugTextCaseExact {
				return strings.Contains(r.Message, q.Text)
			}
			return strings.Contains(strings.ToLower(r.Message), strings.ToLower(q.Text))
		}
		return true
	}
	var page log.JournalPage
	var lastKey string
	scanned := 0
	for i, it := range ord {
		if q.Cursor != "" {
			if s.bug == bugCursorRepeats {
				if it.key > q.Cursor {
					continue
				}
			} else if it.key >= q.Cursor {
				continue
			}
		}
		scanned++
		if s.bug == bugFilterAfterLimit && scanned > limit {
			break
		}
		if !match(it.rec) {
			continue
		}
		if len(page.Records) == limit {
			page.Next = lastKey
			if s.bug == bugCursorSkips && i+1 < len(ord) {
				page.Next = ord[i].key
			}
			return page, nil
		}
		page.Records = append(page.Records, it.rec)
		lastKey = it.key
	}
	if s.bug == bugNextAlwaysSet && len(page.Records) > 0 && len(page.Records) == limit {
		page.Next = lastKey
	}
	if s.bug == bugFilterAfterLimit && scanned > limit && len(page.Records) > 0 {
		page.Next = lastKey
	}
	return page, nil
}

func (s *refStore) Prune(ctx context.Context, before time.Time, limit int) (int, bool, error) {
	if err := s.ctxErr(ctx); err != nil {
		return 0, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ord := s.order() // oldest first
	if s.bug == bugPruneNewest {
		slices.Reverse(ord)
	}
	old := func(it refItem) bool {
		if s.bug == bugPruneInclusive {
			return !it.rec.Time.After(before)
		}
		return it.rec.Time.Before(before)
	}
	var del []string
	more := false
	for _, it := range ord {
		if !old(it) {
			continue
		}
		if len(del) >= limit && s.bug != bugPruneUnbounded {
			more = true
			break
		}
		del = append(del, it.key)
	}
	if s.bug == bugPruneNoMore {
		more = false
	}
	s.items = slices.DeleteFunc(s.items, func(it refItem) bool { return slices.Contains(del, it.key) })
	return len(del), more, nil
}

// recorder is a tester that records failures as "path: message".
type recorder struct {
	path  string
	fails *[]string
}

func newRecorder() *recorder { return &recorder{fails: new([]string)} }

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	*r.fails = append(*r.fails, r.path+": "+fmt.Sprintf(format, args...))
}
func (r *recorder) Run(name string, f func(tester)) bool {
	before := len(*r.fails)
	f(&recorder{path: r.path + name + "/", fails: r.fails})
	return len(*r.fails) == before
}
func (r *recorder) tb() testing.TB {
	panic("the self test opens stores through the recorder, not testing.TB")
}

func runRef(bug refBug) []string {
	r := newRecorder()
	runSuite(r, func(tester) Subject {
		s := &refStore{bug: bug}
		return Subject{Store: s, Importer: s}
	})
	return *r.fails
}

// failedCases is the set of case names that recorded a failure.
func failedCases(fails []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fails {
		c, _, _ := strings.Cut(f, "/")
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

// TestJournalStoreSuite_ReferenceStorePasses: the suite is satisfiable by an
// implementation that shares nothing with core's native store.
func TestJournalStoreSuite_ReferenceStorePasses(t *testing.T) {
	if fails := runRef(bugNone); len(fails) != 0 {
		t.Fatalf("the reference store fails its own contract:\n%s", strings.Join(fails, "\n"))
	}
}

// TestJournalStoreSuite_CatchesEachBrokenStore: every way the reference store
// is broken fails the case that states the rule (and the clean one fails none).
func TestJournalStoreSuite_CatchesEachBrokenStore(t *testing.T) {
	cases := []struct {
		bug  refBug
		want string // a case that must fail
	}{
		{bugOldestFirst, CaseNewestFirst},
		{bugNoDedup, CaseImportIdempotent},
		{bugNoDedup, CaseImportOverlap},
		{bugDedupByTimeOnly, CaseImportDistinctIDs},
		{bugImportsLast, CaseImportSortsByTime},
		{bugPruneNewest, CasePrune},
		{bugPruneUnbounded, CasePrune},
		{bugPruneInclusive, CasePruneBoundary},
		{bugPruneNoMore, CasePrune},
		{bugDropAttrs, CaseRoundTrip},
		{bugTextCaseExact, CaseFilters},
		{bugSinceExclusive, CaseFilters},
		{bugUntilInclusive, CaseFilters},
		{bugIgnoreLevel, CaseFilters},
		{bugIgnoreModule, CaseFilters},
		{bugCursorRepeats, CaseCursor},
		{bugCursorSkips, CaseCursor},
		{bugIgnoreContext, CaseCancelledContext},
		{bugEqualTimeForward, CaseNewestFirst},
		{bugTimeToMillis, CaseRoundTrip},
		{bugNextAlwaysSet, CaseCursor},
		{bugFilterAfterLimit, CaseCursorWithFilter},
	}
	for _, c := range cases {
		t.Run(string(c.bug)+"/"+c.want, func(t *testing.T) {
			got := failedCases(runRef(c.bug))
			if !slices.Contains(got, c.want) {
				t.Errorf("broken store %q: failing cases %q do not include %q", c.bug, got, c.want)
			}
		})
	}
}

// TestJournalStoreSuite_NeedsStoreAndImporter: a Subject missing either half is
// reported, never run as if it passed.
func TestJournalStoreSuite_NeedsStoreAndImporter(t *testing.T) {
	r := newRecorder()
	runSuite(r, func(tester) Subject { return Subject{Store: &refStore{}} })
	if got := failedCases(*r.fails); len(got) != 14 {
		t.Fatalf("a Subject without an Importer failed %d cases, want all 14: %v", len(got), *r.fails)
	}
}

// TestJournalStoreSuite_PassesOnTestingT runs the exported entry point on a
// real *testing.T over the clean store.
func TestJournalStoreSuite_PassesOnTestingT(t *testing.T) {
	JournalStoreSuite(t, func(testing.TB) Subject {
		s := &refStore{}
		return Subject{Store: s, Importer: s}
	})
}
