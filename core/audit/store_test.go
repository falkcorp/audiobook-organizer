// file: core/audit/store_test.go
// version: 1.0.0
// guid: 7e1a9c44-2d5b-4f08-b3a6-91d0c8e5f2a7
// last-edited: 2026-11-10

package audit

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

func success(action string) Event {
	return Event{Action: action, Outcome: Success, Actor: bruce()}
}

// TestAudit_SyncInSameBatch (05-rbac §7.2, COR2-12): RecordIn stages the
// record into the caller's batch, so the record and the change commit
// together, and a batch that is discarded or poisoned leaves neither.
func TestAudit_SyncInSameBatch(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := newStoreOn(t, e, Options{}, Deps{})
	change := []byte("chg/gotham_roster")

	// Committed: the change and its record are both there.
	b := e.ks.NewBatch()
	if err := b.Set(change, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	ev := success("core.role.update")
	ev.Target = entity.Ref{Module: "core", Kind: "role", ID: "robin"}
	if err := s.RecordIn(ctx, b, ev); err != nil {
		t.Fatalf("RecordIn: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatalf("commit: %v", err)
	}
	_ = b.Close()
	if got := all(t, s, Filter{}); len(got) != 1 || got[0].Action != "core.role.update" {
		t.Fatalf("after commit: records = %v, want the one core.role.update", actions(got))
	}
	if ok, err := e.ks.Has(ctx, change); err != nil || !ok {
		t.Fatalf("committed change missing: has=%v err=%v", ok, err)
	}

	// Discarded: the batch is closed without a commit, so neither lands.
	b = e.ks.NewBatch()
	if err := b.Set([]byte("chg/discarded"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIn(ctx, b, success("core.role.delete")); err != nil {
		t.Fatalf("RecordIn into the second batch: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if ok, _ := e.ks.Has(ctx, []byte("chg/discarded")); ok {
		t.Error("discarded batch's change is stored")
	}
	if got := all(t, s, Filter{ActionPrefix: "core.role.delete"}); len(got) != 0 {
		t.Errorf("discarded batch left %d record(s)", len(got))
	}

	// Poisoned: a staged write the batch refuses fails the commit as a whole.
	b = e.ks.NewBatch()
	if err := b.Set([]byte("chg/poisoned"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordIn(ctx, b, success("core.role.create")); err != nil {
		t.Fatalf("RecordIn into the third batch: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := b.Commit(cancelled, storage.Sync); err == nil {
		t.Fatal("commit under a cancelled context succeeded")
	}
	_ = b.Close()
	if ok, _ := e.ks.Has(ctx, []byte("chg/poisoned")); ok {
		t.Error("failed commit left its change")
	}
	if got := all(t, s, Filter{ActionPrefix: "core.role.create"}); len(got) != 0 {
		t.Errorf("failed commit left %d record(s)", len(got))
	}

	// A closed batch refuses the record: the caller fails its operation.
	b = e.ks.NewBatch()
	_ = b.Close()
	if err := s.RecordIn(ctx, b, success("core.role.create")); err == nil {
		t.Error("RecordIn into a closed batch succeeded")
	}
	if err := s.RecordIn(ctx, nil, success("core.role.create")); !errors.Is(err, ErrNilBatch) {
		t.Errorf("RecordIn(nil batch) = %v, want ErrNilBatch", err)
	}

	// Only the first batch's record exists, and the discarded ones left seq gaps.
	got := all(t, s, Filter{})
	if len(got) != 1 {
		t.Fatalf("records = %v, want exactly the committed one", actions(got))
	}
}

// TestAudit_SeqUniqueAcrossCrash (DATA-19, 05-rbac §7.2): a crash between
// allocation and commit never lets a later boot reissue a seq.
func TestAudit_SeqUniqueAcrossCrash(t *testing.T) {
	ctx := context.Background()
	for _, written := range []int{0, 1, 3, 4, 7} {
		t.Run(fmt.Sprintf("written=%d", written), func(t *testing.T) {
			fs := vfs.NewCrashableMem()
			path := filepath.Join(t.TempDir(), "main.pebble")
			e := bootEnv(t, fs, path, 4) // a small lease: refills are frequent
			s := newStoreOn(t, e, Options{}, Deps{})
			var issued []uint64
			for i := range written {
				if err := s.Record(ctx, success("core.auth.login")); err != nil {
					t.Fatalf("Record %d: %v", i, err)
				}
			}
			for _, r := range all(t, s, Filter{}) {
				issued = append(issued, r.Seq)
			}
			// Allocate more ids that never commit: the crash loses them.
			for range 3 {
				b := e.ks.NewBatch()
				if _, err := e.seq.NextIn(ctx, b); err != nil {
					t.Fatal(err)
				}
				_ = b.Close()
			}
			clone := fs.CrashClone(vfs.CrashCloneCfg{})
			e2 := bootEnv(t, clone, path, 4)
			s2 := newStoreOn(t, e2, Options{}, Deps{})
			for i := range 10 {
				if err := s2.Record(ctx, success("core.auth.logout")); err != nil {
					t.Fatalf("Record %d after the crash: %v", i, err)
				}
			}
			recs := all(t, s2, Filter{})
			if len(recs) != written+10 {
				t.Fatalf("records after reboot = %d, want %d", len(recs), written+10)
			}
			seen := map[uint64]bool{}
			for _, r := range recs {
				if r.Seq == 0 || seen[r.Seq] {
					t.Fatalf("seq %d is zero or issued twice (records %v)", r.Seq, seqs(recs))
				}
				seen[r.Seq] = true
			}
			for _, id := range issued {
				if !seen[id] {
					t.Errorf("record %d written before the crash is gone", id)
				}
			}
		})
	}
}

// TestAudit_RedactsSecretsByKeyAndPrefix (05-rbac §7.7): a key that names a
// secret and a value that starts like one of core's credentials are replaced
// by "[redacted]" at any depth; the caller's map is left alone.
func TestAudit_RedactsSecretsByKeyAndPrefix(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := newStoreOn(t, e, Options{}, Deps{})
	details := map[string]any{
		"Password":  "joker123",
		"api_key":   "plain",
		"cookie":    "c=1",
		"note":      "handed over mck_0123456789abcdef to Alfred",
		"bare":      "mcs_sessionvalue",
		"imported":  "abk_oldkey",
		"archive":   "abbs_bootstrap",
		"invite":    "mci_invite",
		"temporary": "mct_login",
		"boot":      "mcb_bootstrap",
		"nested":    map[string]any{"Authorization": "Bearer x", "ok": "Selina Kyle"},
		"list":      []any{map[string]any{"client_secret": "s"}, "mck_inlist", "Dick Grayson"},
		"safe":      "Alfred Pennyworth",
		"count":     42,
	}
	ev := success("core.apikey.create")
	ev.Details = details
	if err := s.Record(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if details["Password"] != "joker123" || details["nested"].(map[string]any)["Authorization"] != "Bearer x" {
		t.Error("the caller's Details map was changed")
	}
	got := all(t, s, Filter{})
	if len(got) != 1 {
		t.Fatalf("records = %d, want 1", len(got))
	}
	d := got[0].Details
	for _, k := range []string{"Password", "api_key", "cookie", "note", "bare", "imported", "archive", "invite", "temporary", "boot"} {
		if d[k] != redacted {
			t.Errorf("Details[%q] = %v, want %q", k, d[k], redacted)
		}
	}
	nested, _ := d["nested"].(map[string]any)
	if nested["Authorization"] != redacted || nested["ok"] != "Selina Kyle" {
		t.Errorf("nested = %v", nested)
	}
	list, _ := d["list"].([]any)
	if len(list) != 3 || list[1] != redacted || list[2] != "Dick Grayson" {
		t.Errorf("list = %v", list)
	} else if inner, _ := list[0].(map[string]any); inner["client_secret"] != redacted {
		t.Errorf("list[0] = %v", list[0])
	}
	if d["safe"] != "Alfred Pennyworth" || fmt.Sprint(d["count"]) != "42" {
		t.Errorf("harmless values changed: safe=%v count=%v", d["safe"], d["count"])
	}
	// The stored bytes never held a secret.
	for kv, err := range e.ks.ScanPrefix(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"joker123", "mck_0123456789abcdef", "mcs_sessionvalue", "abk_oldkey", "Bearer x"} {
			if strings.Contains(string(kv.Value), secret) {
				t.Errorf("row %q stores %q", kv.Key, secret)
			}
		}
	}
}

// TestAudit_DetailsCap: Details over DetailsMaxBytes lose whole keys, in key
// order, and the record says how many; an unserializable value is marked.
func TestAudit_DetailsCap(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := newStoreOn(t, e, Options{DetailsMaxBytes: 256}, Deps{})
	ev := success("core.backup.import")
	ev.Details = map[string]any{
		"a": strings.Repeat("a", 100),
		"b": strings.Repeat("b", 100),
		"c": strings.Repeat("c", 100),
		"d": "short",
	}
	if err := s.Record(ctx, ev); err != nil {
		t.Fatal(err)
	}
	ev = success("core.backup.import")
	ev.Details = map[string]any{"channel": make(chan int)}
	if err := s.Record(ctx, ev); err != nil {
		t.Fatal(err)
	}
	got := all(t, s, Filter{})
	if len(got) != 2 {
		t.Fatalf("records = %d", len(got))
	}
	if got[0].Details[unserializableKey] != true {
		t.Errorf("unserializable Details = %v", got[0].Details)
	}
	capped := got[1].Details
	if _, ok := capped["a"]; !ok {
		t.Errorf("the first key was dropped: %v", capped)
	}
	n, ok := capped[truncatedKey].(float64)
	if !ok || n < 1 {
		t.Fatalf("no %s count in %v", truncatedKey, capped)
	}
	raw, err := jsonLen(capped)
	if err != nil || raw > 256 {
		t.Errorf("capped Details are %d bytes (err %v), cap is 256", raw, err)
	}
	if len(capped)-1+int(n) != 4 {
		t.Errorf("kept %d keys and dropped %v, want 4 in all", len(capped)-1, n)
	}
}

// TestAudit_RecordFields: Record stamps ID, time and seq, fills the actor from
// the context, truncates a long user agent on a rune boundary and rejects an
// event that breaks the rules.
func TestAudit_RecordFields(t *testing.T) {
	ctx := WithActor(context.Background(), bruce())
	e := newEnv(t)
	at := time.Date(2026, 11, 10, 3, 4, 5, 0, time.FixedZone("gotham", -5*3600))
	s := newStoreOn(t, e, Options{}, Deps{Now: func() time.Time { return at }})

	if err := s.Record(ctx, Event{Action: "core.auth.login", Outcome: Success}); err != nil {
		t.Fatal(err)
	}
	long := Actor{UserID: "harvey_dent", UserAgent: strings.Repeat("é", 200)} // 400 bytes
	if err := s.Record(ctx, Event{Action: "core.auth.login", Outcome: Failure, Actor: long, Reason: "bad_password"}); err != nil {
		t.Fatal(err)
	}
	got := all(t, s, Filter{})
	if len(got) != 2 {
		t.Fatalf("records = %d", len(got))
	}
	filled, truncated := got[1], got[0]
	if filled.Actor != bruce() {
		t.Errorf("actor not filled from ctx: %+v", filled.Actor)
	}
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`).MatchString(filled.ID) {
		t.Errorf("ID %q is not a ULID", filled.ID)
	}
	if !filled.Time.Equal(at) || filled.Time.Location() != time.UTC {
		t.Errorf("Time = %v, want %v in UTC", filled.Time, at.UTC())
	}
	if filled.Seq == 0 || truncated.Seq <= filled.Seq {
		t.Errorf("seqs %d, %d: want increasing and non-zero", filled.Seq, truncated.Seq)
	}
	if ua := truncated.Actor.UserAgent; len(ua) > maxUserAgent || len(ua) < maxUserAgent-1 || strings.ContainsRune(ua, '�') {
		t.Errorf("user agent is %d bytes (%q...)", len(ua), ua[:8])
	}

	for name, ev := range map[string]Event{
		"action without a noun": {Action: "core.login", Outcome: Success},
		"upper-case action":     {Action: "core.Auth.login", Outcome: Success},
		"empty action":          {Outcome: Success},
		"unknown outcome":       {Action: "core.auth.login", Outcome: "meh"},
		"no outcome":            {Action: "core.auth.login"},
	} {
		err := s.Record(ctx, ev)
		if !errors.Is(err, ErrInvalidAction) && !errors.Is(err, ErrInvalidOutcome) {
			t.Errorf("%s: Record = %v, want ErrInvalidAction or ErrInvalidOutcome", name, err)
		}
	}
	if n := len(all(t, s, Filter{})); n != 2 {
		t.Errorf("a refused event was stored: %d records", n)
	}
}

// TestAudit_NewWritesNothing: building a store takes no sequence lease and
// writes no row, so booting with it changes no stored state.
func TestAudit_NewWritesNothing(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	before := dump(t, e)
	s := newStoreOn(t, e, Options{}, Deps{})
	_ = all(t, s, Filter{})
	if after := dump(t, e); !slices.Equal(before, after) {
		t.Errorf("New and Query wrote rows:\nbefore %q\nafter  %q", before, after)
	}
	if _, err := New(ctx, Options{}, Deps{}); err == nil {
		t.Error("New without a keyspace succeeded")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(cancelled, Options{}, e.deps()); !errors.Is(err, context.Canceled) {
		t.Errorf("New under a cancelled context = %v", err)
	}
}

// dump lists every key of e's audit keyspace.
func dump(t testing.TB, e *env) []string {
	t.Helper()
	var out []string
	for kv, err := range e.ks.ScanPrefix(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(kv.Key))
	}
	return out
}

// TestAudit_QueryFilters (05-rbac §7.6): Query by actor, action prefix,
// outcome, target (every prefix of it) and time, newest first, and paged by
// the BeforeSeq cursor.
func TestAudit_QueryFilters(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var tick int
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	s := newStoreOn(t, e, Options{}, Deps{Now: func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Hour) }})

	bruceA, selina := bruce(), Actor{UserID: "selina_kyle", Username: "Selina Kyle"}
	rec := func(action string, o Outcome, a Actor, target entity.Ref) {
		t.Helper()
		if err := s.Record(ctx, Event{Action: action, Outcome: o, Actor: a, Target: target}); err != nil {
			t.Fatalf("Record %s: %v", action, err)
		}
	}
	robin := entity.Ref{Module: "core", Kind: "role", ID: "robin"}
	nightwing := entity.Ref{Module: "core", Kind: "role", ID: "nightwing"}
	vehicle := entity.Ref{Module: "books", Kind: "vehicle", ID: "batmobile"}
	rec("core.auth.login", Success, bruceA, entity.Ref{})  // 1
	rec("core.auth.login", Failure, selina, entity.Ref{})  // 2
	rec("core.role.create", Success, bruceA, robin)        // 3
	rec("core.role.update", Success, bruceA, robin)        // 4
	rec("core.role.update", Denied, selina, nightwing)     // 5
	rec("books.vehicle.merge", Success, bruceA, vehicle)   // 6
	rec("core.role.delete", Error, Actor{}, nightwing)     // 7 (anonymous)
	rec("core.auth.logout", Success, bruceA, entity.Ref{}) // 8

	byTime := all(t, s, Filter{})
	if len(byTime) != 8 || !sortedDesc(seqs(byTime)) {
		t.Fatalf("unfiltered: %d records, seqs %v: want 8, newest first", len(byTime), seqs(byTime))
	}
	at := func(i int) time.Time { return byTime[len(byTime)-i].Time } // the i-th written

	tests := []struct {
		name string
		f    Filter
		want []string // actions, newest first
	}{
		{"actor", Filter{ActorUserID: "bruce_wayne"}, []string{"core.auth.logout", "books.vehicle.merge", "core.role.update", "core.role.create", "core.auth.login"}},
		{"another actor", Filter{ActorUserID: "selina_kyle"}, []string{"core.role.update", "core.auth.login"}},
		{"unknown actor", Filter{ActorUserID: "oswald_cobblepot"}, nil},
		{"action prefix", Filter{ActionPrefix: "core.role."}, []string{"core.role.delete", "core.role.update", "core.role.update", "core.role.create"}},
		{"namespace prefix", Filter{ActionPrefix: "core."}, []string{"core.auth.logout", "core.role.delete", "core.role.update", "core.role.update", "core.role.create", "core.auth.login", "core.auth.login"}},
		{"whole action", Filter{ActionPrefix: "core.role.update"}, []string{"core.role.update", "core.role.update"}},
		{"prefix outside the alphabet", Filter{ActionPrefix: "core.Role"}, nil},
		{"outcome", Filter{Outcome: Denied}, []string{"core.role.update"}},
		{"outcome with prefix", Filter{Outcome: Success, ActionPrefix: "core.role."}, []string{"core.role.update", "core.role.create"}},
		{"target module", Filter{Target: entity.Ref{Module: "core"}}, []string{"core.role.delete", "core.role.update", "core.role.update", "core.role.create"}},
		{"target kind", Filter{Target: entity.Ref{Module: "books", Kind: "vehicle"}}, []string{"books.vehicle.merge"}},
		{"target id", Filter{Target: robin}, []string{"core.role.update", "core.role.create"}},
		{"target and actor", Filter{Target: nightwing, ActorUserID: "selina_kyle"}, []string{"core.role.update"}},
		{"target and outcome", Filter{Target: nightwing, Outcome: Error}, []string{"core.role.delete"}},
		{"since", Filter{Since: at(3)}, []string{"core.auth.logout", "core.role.delete", "books.vehicle.merge", "core.role.update", "core.role.update", "core.role.create"}},
		{"until", Filter{Until: at(2)}, []string{"core.auth.login", "core.auth.login"}},
		{"since and until", Filter{Since: at(3), Until: at(5)}, []string{"core.role.update", "core.role.update", "core.role.create"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := all(t, s, tc.f)
			if !slices.Equal(actions(got), tc.want) {
				t.Errorf("actions = %v\nwant     %v", actions(got), tc.want)
			}
			if !sortedDesc(seqs(got)) {
				t.Errorf("seqs %v are not newest first", seqs(got))
			}
		})
	}

	t.Run("cursor pages", func(t *testing.T) {
		for name, f := range map[string]Filter{
			"all records":   {},
			"one actor":     {ActorUserID: "bruce_wayne"},
			"a full target": {Target: nightwing},
			"a prefix":      {ActionPrefix: "core."},
		} {
			want := seqs(all(t, s, f))
			var paged []uint64
			var cursor uint64
			for range 20 {
				f.Limit, f.BeforeSeq = 2, cursor
				var page []Record
				for r, err := range s.Query(ctx, f) {
					if err != nil {
						t.Fatal(err)
					}
					page = append(page, r)
				}
				if len(page) == 0 {
					break
				}
				if len(page) > 2 {
					t.Fatalf("%s: page of %d, limit 2", name, len(page))
				}
				paged = append(paged, seqs(page)...)
				cursor = page[len(page)-1].Seq
			}
			if !slices.Equal(paged, want) {
				t.Errorf("%s: paging gave %v, want %v", name, paged, want)
			}
		}
	})

	t.Run("limit", func(t *testing.T) {
		n := 0
		for _, err := range s.Query(ctx, Filter{}) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		if n != 8 {
			t.Errorf("default limit returned %d of 8", n)
		}
		n = 0
		for _, err := range s.Query(ctx, Filter{Limit: 3}) {
			if err != nil {
				t.Fatal(err)
			}
			n++
		}
		if n != 3 {
			t.Errorf("Limit 3 returned %d", n)
		}
	})

	t.Run("early break", func(t *testing.T) {
		for range s.Query(ctx, Filter{}) {
			break
		}
	})
}

// TestAudit_ConcurrentUse: Record, RecordIn, Emit and Query are safe from many
// goroutines at once (run under -race), every seq is unique and nothing
// written is lost.
func TestAudit_ConcurrentUse(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := newStoreOn(t, e, Options{AsyncBuffer: 1024, BatchWait: time.Millisecond}, Deps{})
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	const workers, each = 8, 25
	var wg sync.WaitGroup
	errc := make(chan error, workers*each*2)
	for w := range workers {
		wg.Go(func() {
			for i := range each {
				ev := Event{Action: "core.auth.login", Outcome: Success, Actor: Actor{UserID: fmt.Sprintf("robin_%d", w)}}
				switch i % 3 {
				case 0:
					errc <- s.Record(ctx, ev)
				case 1:
					b := e.ks.NewBatch()
					err := s.RecordIn(ctx, b, ev)
					if err == nil {
						err = b.Commit(ctx, storage.Sync)
					}
					_ = b.Close()
					errc <- err
				default:
					ev.Outcome = Failure
					s.Emit(ctx, ev)
				}
				for _, err := range s.Query(ctx, Filter{Limit: 5}) {
					if err != nil {
						errc <- err
					}
				}
			}
		})
	}
	wg.Wait()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	got := all(t, s, Filter{})
	if want := workers * each; len(got) != want {
		t.Fatalf("%d records, want %d (none dropped: %d)", len(got), want, s.ctr.dropped.Load())
	}
	if !sortedDesc(seqs(got)) {
		t.Error("seqs are not unique and descending")
	}
}
