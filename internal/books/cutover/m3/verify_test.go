// file: internal/books/cutover/m3/verify_test.go
// version: 1.0.0
// guid: 68d24b6a-0ed7-4e5e-9a8b-319dccdacfdf
// last-edited: 2026-11-12

package m3

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// memReader is a sorted in-memory storage.Reader: enough of one for Verify and
// the preflight, which only Get and Scan forward.
type memReader map[string]string

func (m memReader) Get(_ context.Context, key []byte) ([]byte, error) {
	v, ok := m[string(key)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return []byte(v), nil
}

func (m memReader) Scan(_ context.Context, o storage.ScanOptions) iter.Seq2[storage.KV, error] {
	return func(yield func(storage.KV, error) bool) {
		n := 0
		for _, k := range slices.Sorted(maps.Keys(m)) {
			if k < string(o.Start) || (o.End != nil && k >= string(o.End)) {
				continue
			}
			kv := storage.KV{Key: []byte(k)}
			if !o.KeysOnly {
				kv.Value = []byte(m[k])
			}
			if !yield(kv, nil) {
				return
			}
			if n++; o.Limit > 0 && n >= o.Limit {
				return
			}
		}
	}
}

func (memReader) NewIterator(context.Context, storage.ScanOptions) (*storage.Iterator, error) {
	return nil, errors.New("memReader: no iterator")
}

// nativeUsers is a NativeReader over a set of user IDs.
type nativeUsers map[string]bool

func (n nativeUsers) GetUser(_ context.Context, id auth.UserID) (*auth.User, error) {
	if n[string(id)] {
		return &auth.User{ID: id}, nil
	}
	return nil, fmt.Errorf("user %s: %w", id, auth.ErrNotFound)
}

func (nativeUsers) GetRole(_ context.Context, name rbac.RoleName) (*rbac.Role, error) {
	return nil, fmt.Errorf("role %s: %w", name, rbac.ErrUnknownRole)
}

// brokenNative fails every lookup with an error that is not "not found".
type brokenNative struct{ nativeUsers }

func (brokenNative) GetUser(context.Context, auth.UserID) (*auth.User, error) {
	return nil, errors.New("the user store is unreachable")
}

const (
	vKnown  = "01M7ZZZVERIFYKNOWN0000001"
	vKnown2 = "01M7ZZZVERIFYKNOWN0000002"
	vGhost  = "01M7ZZZVERIFYGHOST0000001"
	vGhost2 = "01M7ZZZVERIFYGHOST0000002"
)

func verifyOver(t *testing.T, rows memReader, native NativeReader) (failures []string, orphans map[string]int64, err error) {
	t.Helper()
	m := testMigrationOver(t, native, func() storage.Reader { return rows })
	rep, err := m.Verify(context.Background(), rows)
	return rep.Failures, rep.OrphanedRefs, err
}

// TestVerify_CleanStore: nothing under Source, no bootstrap or expiry row, no
// raw token in a journal, every referenced user native: no failure, no orphan.
func TestVerify_CleanStore(t *testing.T) {
	rows := memReader{
		"ubs:" + vKnown + ":book1":          "1",
		"upl:p1":                            `{"created_by_user_id":"` + vKnown + `"}`,
		"usermerge:journal:j1":              `{"status":"applied","ops":[{"key":"invite-sha256:` + strings.Repeat("a", 64) + `"}]}`,
		"setting:startup_readonly_key_a":    "left alone by row 16",
		"idx:book:title:the-long-halloween": "x", // another family's idx: rows are never Source
	}
	failures, orphans, err := verifyOver(t, rows, nativeUsers{vKnown: true})
	if err != nil || len(failures) != 0 || len(orphans) != 0 {
		t.Fatalf("failures %v, orphans %v, err %v", failures, orphans, err)
	}
}

// TestVerify_Failures: one leftover row under any Source range, any of the
// three exact rows, or a raw token in a journal fails the step.
func TestVerify_Failures(t *testing.T) {
	known := nativeUsers{vKnown: true}
	for _, p := range SourcePrefixes() {
		failures, _, err := verifyOver(t, memReader{p + "leftover": "x"}, known)
		if err != nil || len(failures) != 1 || !strings.Contains(failures[0], "rows are still present under "+p) {
			t.Errorf("a row under %s: failures %v, err %v", p, failures, err)
		}
	}
	for _, k := range append(SourceKeys(), expiryDoneKey) {
		failures, _, err := verifyOver(t, memReader{k: "x"}, known)
		if err != nil || len(failures) != 1 || !strings.Contains(failures[0], k) {
			t.Errorf("the row %s: failures %v, err %v", k, failures, err)
		}
	}
	raw := `{"status":"applied","ops":[{"key":"invite:mck-riddler-0001","family":"invites"}]}`
	failures, _, err := verifyOver(t, memReader{"usermerge:journal:jx": raw}, known)
	if err != nil || len(failures) != 1 || !strings.Contains(failures[0], "jx") || strings.Contains(failures[0], "mck-riddler") {
		t.Errorf("a journal with a raw token: failures %v, err %v (the failure names the journal, never the token)", failures, err)
	}
	// A lookup that fails for a reason other than "not found" fails Verify itself.
	if _, _, err := verifyOver(t, memReader{"ubs:" + vKnown + ":b": "1"}, brokenNative{}); err == nil {
		t.Errorf("a native lookup that errors was read as an absent user")
	}
}

// TestVerify_OrphanedRefs (DATA-2): a books row that names a user the native
// store does not hold is counted under its family, in every shape the merge's
// list has (user embedded in the key, exact key, owner field of a JSON
// value), and never fails the step; a row of a native user is not counted.
func TestVerify_OrphanedRefs(t *testing.T) {
	rows := memReader{
		// key-embedded: ghosts interleaved with a known user, and a user whose ID
		// is a prefix of another's (the seek past a user's rows must not skip the next).
		"ubs:" + vGhost + ":b1":           "1",
		"ubs:" + vGhost + ":b2":           "1",
		"ubs:" + vGhost + "X:b3":          "1",
		"ubs:" + vKnown + ":b1":           "1",
		"ubs:" + vKnown + ":b2":           "1",
		"ubs:" + vGhost2 + ":b1":          "1",
		"upos:" + vGhost + ":b1":          "1",
		"upos:" + vKnown + ":b1":          "1",
		"bookmark:" + vGhost + ":b1:5":    "1",
		"sync_alias_use:" + vGhost + ":a": "1",
		"pref:" + vGhost + ":theme":       "1",
		"playp:" + vGhost + ":p1":         "1",
		"playe:" + vGhost + ":p1:e1":      "1",
		"playe:" + vKnown + ":p1:e1":      "1",
		// exact keys
		"sync_alias_use_seeded:" + vGhost:     "1",
		"sync_alias_use_seeded:" + vKnown:     "1",
		"stats:user:listen_seconds:" + vGhost: "9",
		"stats:user:listen_seconds:" + vKnown: "9",
		// owner field
		"upl:p1": `{"created_by_user_id":"` + vGhost + `"}`,
		"upl:p2": `{"created_by_user_id":"` + vKnown + `"}`,
		"upl:p3": `{"created_by_user_id":""}`,
		"upl:p4": `{"name":"no owner field"}`,
		"upl:p5": "not json",
		"col:c1": `{"created_by_user_id":"` + vGhost2 + `"}`,
	}
	failures, orphans, err := verifyOver(t, rows, nativeUsers{vKnown: true, vKnown2: true})
	if err != nil || len(failures) != 0 {
		t.Fatalf("failures %v, err %v: an orphaned reference never fails the step", failures, err)
	}
	want := map[string]int64{
		"ubs:": 4, "upos:": 1, "bookmark:": 1, "sync_alias_use:": 1, "pref:": 1, "playp:": 1, "playe:": 1,
		"sync_alias_use_seeded:": 1, "stats:user:listen_seconds:": 1, "upl:": 1, "col:": 1,
	}
	if !maps.Equal(orphans, want) {
		t.Errorf("orphans %v, want %v", orphans, want)
	}
}

func TestRefFamilyPrefixes(t *testing.T) {
	got := RefFamilyPrefixes()
	if len(got) != len(refFamilies) || len(got) != 11 {
		t.Fatalf("RefFamilyPrefixes = %v", got)
	}
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] || !strings.HasSuffix(p, ":") {
			t.Errorf("prefix %q repeated or not family-shaped", p)
		}
		seen[p] = true
	}
}
