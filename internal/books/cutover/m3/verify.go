// file: internal/books/cutover/m3/verify.go
// version: 1.0.0
// guid: b92d0fa5-1faa-4278-b69f-3bd855ad6d6e
// last-edited: 2026-11-12

package m3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// refFamily is a books family whose rows name a user: <prefix><user>:<rest>
// (or <prefix><user> alone when exact), or a JSON row with an owner field.
// The key-embedded list is the one the user merge rekeys
// (store.userScopedPrefixes and userScopedExact, user_merge.go); the owner-
// field rows are its playlists and collections (created_by_user_id). The
// census test pins the list against that source.
type refFamily struct {
	prefix string
	exact  bool   // the key is prefix + user and nothing more
	field  string // a JSON owner field of the row's value; "" = the key embeds the user
}

var refFamilies = []refFamily{
	{prefix: "ubs:"}, {prefix: "upos:"}, {prefix: "bookmark:"}, {prefix: "sync_alias_use:"},
	{prefix: "pref:"}, {prefix: "playp:"}, {prefix: "playe:"},
	{prefix: "sync_alias_use_seeded:", exact: true}, {prefix: "stats:user:listen_seconds:", exact: true},
	{prefix: "upl:", field: "created_by_user_id"}, {prefix: "col:", field: "created_by_user_id"},
}

// RefFamilyPrefixes are the prefixes of the families Verify's referential pass
// reads, for the census that checks them against the merge's list.
func RefFamilyPrefixes() []string {
	out := make([]string, 0, len(refFamilies))
	for _, f := range refFamilies {
		out = append(out, f.prefix)
	}
	return out
}

// Verify is the step's invariant gate, over the store alone: every Source
// range is empty, the bootstrap hash and HEAD's expiry flag are gone, no merge
// journal keeps a raw invite token in a key image, and every user ID a books
// row names resolves to a native user. A reference to a user that does not
// (a blocker the owner deleted) is counted in OrphanedRefs per referencing
// family and never fails the step (DATA-2).
func (m *Migration) Verify(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
	var rep datamigrate.VerifyReport
	for _, p := range identityPrefixes {
		for _, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte(p), End: prefixEnd(p), KeysOnly: true, Limit: 1}) {
			if err != nil {
				return rep, err
			}
			rep.Failures = append(rep.Failures, fmt.Sprintf("rows are still present under %s", p))
		}
	}
	// The two bootstrap rows are Source keys (ownership table row 8) and the
	// expiry flag is the one row outside Source that M3 deletes (row 16).
	for _, key := range append(SourceKeys(), expiryDoneKey) {
		switch _, err := r.Get(ctx, []byte(key)); {
		case errors.Is(err, storage.ErrNotFound):
		case err != nil:
			return rep, err
		default:
			rep.Failures = append(rep.Failures, fmt.Sprintf("the %s row is still present", key))
		}
	}
	journals, err := scanRows(ctx, r, JournalPrefix)
	if err != nil {
		return rep, err
	}
	for _, kv := range journals {
		j, derr := decodeJournal(kv.Value)
		if derr != nil {
			continue
		}
		if len(j.tokens()) > 0 {
			rep.Failures = append(rep.Failures, fmt.Sprintf("the journal %s keeps a raw invite token", strings.TrimPrefix(string(kv.Key), JournalPrefix)))
		}
	}
	orphans, err := m.referentialPass(ctx, r)
	if err != nil {
		return rep, err
	}
	rep.OrphanedRefs = orphans
	return rep, nil
}

// referentialPass counts, per referencing family, the rows that name a user
// the native store does not hold. The families are read concurrently, one
// worker each, bounded by the CPU count: they are independent reads, and a
// library's per-user families (positions, preferences, events) are the one
// library-scale scan in the step. A key-embedded family is read a user at a
// time (a seek past each resolved user's rows) so a million rows of a few
// users cost a few lookups.
func (m *Migration) referentialPass(ctx context.Context, r storage.Reader) (map[string]int64, error) {
	res := &resolver{m: m, known: map[string]bool{}}
	var (
		mu  sync.Mutex
		out = map[string]int64{}
	)
	var g errgroup.Group
	g.SetLimit(min(runtime.NumCPU(), len(refFamilies)))
	for _, f := range refFamilies {
		g.Go(func() error {
			n, err := m.orphansIn(ctx, r, res, f)
			if err != nil {
				return err
			}
			if n > 0 {
				mu.Lock()
				out[f.prefix] = n
				mu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// resolver memoizes which user IDs the native store holds.
type resolver struct {
	m     *Migration
	mu    sync.Mutex
	known map[string]bool
}

func (r *resolver) has(ctx context.Context, id string) (bool, error) {
	r.mu.Lock()
	ok, seen := r.known[id]
	r.mu.Unlock()
	if seen {
		return ok, nil
	}
	_, err := r.m.cfg.Native.GetUser(ctx, auth.UserID(id))
	switch {
	case err == nil:
		ok = true
	case errors.Is(err, auth.ErrNotFound):
		ok = false
	default:
		return false, err
	}
	r.mu.Lock()
	r.known[id] = ok
	r.mu.Unlock()
	return ok, nil
}

// orphansIn counts family f's rows whose user is not native.
func (m *Migration) orphansIn(ctx context.Context, r storage.Reader, res *resolver, f refFamily) (int64, error) {
	end := prefixEnd(f.prefix)
	var n int64
	switch {
	case f.field != "":
		for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte(f.prefix), End: end}) {
			if err != nil {
				return 0, err
			}
			var row map[string]json.RawMessage
			if json.Unmarshal(kv.Value, &row) != nil {
				continue
			}
			var id string
			if raw, ok := row[f.field]; !ok || json.Unmarshal(raw, &id) != nil || id == "" {
				continue
			}
			ok, err := res.has(ctx, id)
			if err != nil {
				return 0, err
			}
			if !ok {
				n++
			}
		}
	case f.exact:
		for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte(f.prefix), End: end, KeysOnly: true}) {
			if err != nil {
				return 0, err
			}
			ok, err := res.has(ctx, strings.TrimPrefix(string(kv.Key), f.prefix))
			if err != nil {
				return 0, err
			}
			if !ok {
				n++
			}
		}
	default:
		start := []byte(f.prefix)
		for {
			var first []byte
			for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: start, End: end, KeysOnly: true, Limit: 1}) {
				if err != nil {
					return 0, err
				}
				first = kv.Key
				first = append([]byte(nil), first...)
			}
			if first == nil {
				return n, nil
			}
			rest := strings.TrimPrefix(string(first), f.prefix)
			uid, _, _ := strings.Cut(rest, ":")
			own := []byte(f.prefix + uid + ":")
			past := prefixEnd(f.prefix + uid + ":")
			ok, err := res.has(ctx, uid)
			if err != nil {
				return 0, err
			}
			if !ok {
				for _, err := range r.Scan(ctx, storage.ScanOptions{Start: own, End: past, KeysOnly: true}) {
					if err != nil {
						return 0, err
					}
					n++
				}
			}
			start = past
		}
	}
	return n, nil
}
