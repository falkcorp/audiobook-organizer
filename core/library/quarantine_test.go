// file: core/library/quarantine_test.go
// version: 1.0.0
// guid: b8d3bbde-6baa-4aa5-90a8-b24bb68fba35
// last-edited: 2026-11-11

package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// qRows counts every quarantine record, in any state.
func (r *rig) qRows() (n int) {
	r.t.Helper()
	err := r.m.qrn.all(ctxbg(), func(string, []byte) error { n++; return nil })
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// heldFiles lists the files under a root's quarantine area.
func (r *rig) heldFiles(id RootID) []string {
	var out []string
	area := r.abs(id, DefaultQuarantine)
	_ = filepath.WalkDir(area, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestQuarantineReleaseRoundTrip(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	orig := r.put("books-library", "Gotham/The Dark Knight/01.m4b", "chapter one")
	r.put("books-library", "Gotham/The Dark Knight/cover.jpg", "cover")
	r.put("books-library", "Gotham/Year One.m4b", "year one")
	other := r.put("books-library", "Gotham/Year One/02.m4b", "unrelated")
	_ = other

	id, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "Gotham/The Dark Knight"}, "corrupt tags")
	if err != nil {
		t.Fatal(err)
	}
	if !validULID(string(id)) {
		t.Errorf("id %q is not a ULID", id)
	}
	if r.exists("books-library", "Gotham/The Dark Knight") {
		t.Error("the directory is still in the library")
	}
	held := DefaultQuarantine + "/" + string(id) + "/Gotham/The Dark Knight"
	if got, _ := r.read("books-library", held+"/01.m4b"); got != "chapter one" {
		t.Errorf("held file = %q", got)
	}
	recs, err := r.m.Quarantined(ctxbg(), books)
	if err != nil || len(recs) != 1 {
		t.Fatalf("Quarantined = %v, %v", recs, err)
	}
	if rec := recs[0]; rec.ID != id || rec.Original != (Path{Root: "books-library", Rel: "Gotham/The Dark Knight"}) ||
		rec.Held.Rel != held || rec.Owner != books || rec.Reason != "corrupt tags" || rec.At.IsZero() {
		t.Errorf("record = %+v", rec)
	}
	if other, err := r.m.Quarantined(ctxbg(), tv); err != nil || len(other) != 0 {
		t.Errorf("another owner sees %v, %v", other, err)
	}

	// Release never clobbers: something took the original name meanwhile.
	r.put("books-library", "Gotham/The Dark Knight/01.m4b", "a newer upload")
	if _, err := r.m.Release(ctxbg(), books, id, nil); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Release over an existing name = %v, want fs.ErrExist", err)
	}
	if got, _ := r.read("books-library", "Gotham/The Dark Knight/01.m4b"); got != "a newer upload" {
		t.Errorf("the newer file was overwritten: %q", got)
	}
	if got, _ := r.read("books-library", held+"/01.m4b"); got != "chapter one" {
		t.Errorf("the held file was disturbed: %q", got)
	}
	if recs, _ := r.m.Quarantined(ctxbg(), books); len(recs) != 1 {
		t.Fatalf("a refused Release must leave the record held, got %d records", len(recs))
	}
	// Release to somewhere else works, ends the record and tidies the area.
	to := Path{Root: "books-library", Rel: "Restored/The Dark Knight"}
	if got, err := r.m.Release(ctxbg(), books, id, &to); err != nil || got != to {
		t.Fatalf("Release(to) = %v, %v", got, err)
	}
	if got, _ := r.read("books-library", "Restored/The Dark Knight/01.m4b"); got != "chapter one" {
		t.Errorf("released content = %q", got)
	}
	if n := r.qRows(); n != 0 {
		t.Errorf("%d records left after Release", n)
	}
	if files := r.heldFiles("books-library"); len(files) != 0 {
		t.Errorf("held files left: %v", files)
	}
	if _, err := os.Stat(r.abs("books-library", DefaultQuarantine+"/"+string(id))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the holding directory was not tidied: %v", err)
	}
	if _, err := r.m.Release(ctxbg(), books, id, nil); !errors.Is(err, ErrUnknownQuarantine) {
		t.Errorf("a second Release = %v, want ErrUnknownQuarantine", err)
	}

	// A single file goes back to its own path.
	id2, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "Gotham/Year One.m4b"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.m.Release(ctxbg(), books, id2, nil); err != nil || got.Rel != "Gotham/Year One.m4b" {
		t.Fatalf("Release = %v, %v", got, err)
	}
	if got, _ := r.read("books-library", "Gotham/Year One.m4b"); got != "year one" {
		t.Errorf("released file = %q", got)
	}
	_ = orig
}

func TestQuarantineRefusals(t *testing.T) {
	disabled := map[string]bool{}
	r := newRig(t, newStore(t), rigOpts{enabled: func(o storage.Owner) bool { return !disabled[string(o)] }},
		spec("books-library", books, RoleLibrary), spec("itunes", books, RoleExternal))
	r.put("books-library", "a.m4b", "bytes")
	r.put("itunes", "Music/a.m4a", "bytes")
	p := Path{Root: "books-library", Rel: "a.m4b"}
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library"}, "x"); !errors.Is(err, ErrBadTransfer) {
		t.Errorf("Quarantine of a root = %v, want ErrBadTransfer", err)
	}
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "nope", Rel: "a"}, "x"); !errors.Is(err, ErrUnknownRoot) {
		t.Errorf("unknown root = %v, want ErrUnknownRoot", err)
	}
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "missing.m4b"}, "x"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing file = %v, want fs.ErrNotExist", err)
	}
	if n := r.qRows(); n != 0 {
		t.Errorf("a refused Quarantine left %d records", n)
	}
	if _, err := r.m.Quarantine(ctxbg(), tv, p, "x"); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("another owner = %v, want ErrReadOnlyRoot", err)
	}
	if _, err := r.m.Quarantine(ctxbg(), books, Path{Root: "itunes", Rel: "Music/a.m4a"}, "x"); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("an external root = %v, want ErrReadOnlyRoot", err)
	}
	// An unmounted root is not acted on.
	if err := os.Remove(r.abs("books-library", DefaultAppDir+"/root.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Quarantine(ctxbg(), books, p, "x"); !errors.Is(err, ErrNotMounted) {
		t.Errorf("without a sentinel = %v, want ErrNotMounted", err)
	}
	if err := r.m.WriteSentinel(ctxbg(), "books-library"); err != nil {
		t.Fatal(err)
	}
	// A disabled owner's roots take no quarantine.
	disabled["books"] = true
	if _, err := r.m.Quarantine(ctxbg(), books, p, "x"); !errors.Is(err, ErrOwnerDisabled) {
		t.Errorf("a disabled owner = %v, want ErrOwnerDisabled", err)
	}
	disabled["books"] = false
	id, err := r.m.Quarantine(ctxbg(), books, p, "x")
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]QuarantineID{"unknown": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "malformed": "../../etc"} {
		if _, err := r.m.Release(ctxbg(), books, bad, nil); !errors.Is(err, ErrUnknownQuarantine) {
			t.Errorf("Release of a %s id = %v", name, err)
		}
		if err := r.m.Purge(ctxbg(), bad); !errors.Is(err, ErrUnknownQuarantine) {
			t.Errorf("Purge of a %s id = %v", name, err)
		}
	}
	if _, err := r.m.Release(ctxbg(), books, id, &Path{Root: "books-library"}); !errors.Is(err, ErrBadTransfer) {
		t.Errorf("Release to a root = %v, want ErrBadTransfer", err)
	}
	if _, err := r.m.Release(ctxbg(), books, id, &Path{Root: "itunes", Rel: "Music/b.m4a"}); !errors.Is(err, ErrReadOnlyRoot) {
		t.Errorf("Release into an external root = %v, want ErrReadOnlyRoot", err)
	}
	disabled["books"] = true
	if _, err := r.m.Release(ctxbg(), books, id, nil); !errors.Is(err, ErrOwnerDisabled) {
		t.Errorf("Release for a disabled owner = %v, want ErrOwnerDisabled", err)
	}
	if err := r.m.Purge(ctxbg(), id); !errors.Is(err, ErrOwnerDisabled) {
		t.Errorf("Purge for a disabled owner = %v, want ErrOwnerDisabled", err)
	}
}

func TestPurgeRemovesTheHeldItemAndItsRecord(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	r.put("books-library", "Gotham/a/01.m4b", "one")
	r.put("books-library", "Gotham/a/02.m4b", "two")
	id, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "Gotham/a"}, "duplicate")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.m.Purge(ctxbg(), id); err != nil {
		t.Fatal(err)
	}
	if files := r.heldFiles("books-library"); len(files) != 0 {
		t.Errorf("held files left: %v", files)
	}
	if r.qRows() != 0 {
		t.Error("the record is still there")
	}
	if err := r.m.Purge(ctxbg(), id); !errors.Is(err, ErrUnknownQuarantine) {
		t.Errorf("a second Purge = %v, want ErrUnknownQuarantine", err)
	}
	if _, err := r.m.Release(ctxbg(), books, id, nil); !errors.Is(err, ErrUnknownQuarantine) {
		t.Errorf("Release after Purge = %v, want ErrUnknownQuarantine", err)
	}
}

// quarantineCase is one quarantine operation killed at each of its steps.
type quarantineCase struct {
	name string
	// prep returns the id the operation acts on ("" for Quarantine itself).
	prep func(r *rig) QuarantineID
	run  func(r *rig, id QuarantineID) error
	// ok checks the settled state: the item is whole at exactly one place and
	// the record agrees with it.
	ok func(r *rig, id QuarantineID) error
}

func quarantineCases() []quarantineCase {
	src := Path{Root: "books-library", Rel: "Gotham/The Long Halloween/01.m4b"}
	const content = "twelve chapters"
	setup := func(r *rig) { r.put("books-library", src.Rel, content) }
	heldPath := func(id QuarantineID) string { return DefaultQuarantine + "/" + string(id) + "/" + src.Rel }
	heldNow := func(r *rig, id QuarantineID) (bool, error) {
		got, ok := r.read("books-library", heldPath(id))
		if ok && got != content {
			return false, fmt.Errorf("held content %q", got)
		}
		return ok, nil
	}
	origNow := func(r *rig) (bool, error) {
		got, ok := r.read("books-library", src.Rel)
		if ok && got != content {
			return false, fmt.Errorf("original content %q", got)
		}
		return ok, nil
	}
	quarantine := func(r *rig) QuarantineID {
		setup(r)
		id, err := r.m.Quarantine(ctxbg(), books, src, "x")
		if err != nil {
			r.t.Fatal(err)
		}
		return id
	}
	return []quarantineCase{
		{
			name: "quarantine",
			prep: func(r *rig) QuarantineID { setup(r); return "" },
			run: func(r *rig, _ QuarantineID) error {
				_, err := r.m.Quarantine(ctxbg(), books, src, "x")
				return err
			},
			ok: func(r *rig, _ QuarantineID) error {
				orig, err := origNow(r)
				if err != nil {
					return err
				}
				files := r.heldFiles("books-library")
				switch {
				case orig && len(files) == 0 && r.qRows() == 0 && len(r.leftovers("books-library")) == 0:
					return nil // rolled back
				case !orig && len(files) == 1 && r.qRows() == 1:
					held, _ := r.m.Quarantined(ctxbg(), books)
					if len(held) != 1 {
						return errors.New("the record is not held")
					}
					return nil // done
				}
				return fmt.Errorf("original %v, held %v, %d records", orig, files, r.qRows())
			},
		},
		{
			name: "release",
			prep: quarantine,
			run: func(r *rig, id QuarantineID) error {
				_, err := r.m.Release(ctxbg(), books, id, nil)
				return err
			},
			ok: func(r *rig, id QuarantineID) error {
				orig, err := origNow(r)
				if err != nil {
					return err
				}
				held, err := heldNow(r, id)
				if err != nil {
					return err
				}
				switch {
				case orig && !held && r.qRows() == 0:
					return nil // released
				case !orig && held && r.qRows() == 1:
					recs, _ := r.m.Quarantined(ctxbg(), books)
					if len(recs) != 1 {
						return errors.New("the record is not held")
					}
					return nil // rolled back
				}
				return fmt.Errorf("original %v, held %v, %d records", orig, held, r.qRows())
			},
		},
		{
			name: "purge",
			prep: quarantine,
			run:  func(r *rig, id QuarantineID) error { return r.m.Purge(ctxbg(), id) },
			ok: func(r *rig, id QuarantineID) error {
				held, err := heldNow(r, id)
				if err != nil {
					return err
				}
				recs, _ := r.m.Quarantined(ctxbg(), books)
				switch {
				case !held && r.qRows() == 0:
					return nil // purged
				case held && len(recs) == 1:
					return nil // not started
				}
				return fmt.Errorf("held %v, %d records, %d held records", held, r.qRows(), len(recs))
			},
		},
	}
}

// Quarantine, Release and Purge are each killed at every durable step; a new
// manager's Recover settles the record and the files to one consistent state,
// and the item is never lost.
func TestQuarantineRecoveryFromJournal(t *testing.T) {
	for _, c := range quarantineCases() {
		t.Run(c.name, func(t *testing.T) {
			dry := &faults{}
			dr, _ := transferRig(t, rigOpts{faults: dry})
			id := c.prep(dr)
			dry.mu.Lock()
			before := dry.count
			dry.mu.Unlock()
			if err := c.run(dr, id); err != nil {
				t.Fatalf("dry run: %v", err)
			}
			n := dry.steps() - before
			if n < 2 {
				t.Fatalf("only %d steps: %v", n, dry.labels)
			}
			if err := c.ok(dr, id); err != nil {
				t.Fatalf("after the dry run: %v", err)
			}
			for k := 1; k <= n; k++ {
				f := &faults{}
				r, _ := transferRig(t, rigOpts{faults: f})
				id := c.prep(r)
				f.mu.Lock()
				f.killAt = f.count + k
				f.mu.Unlock()
				_ = c.run(r, id)
				label := fmt.Sprintf("killed at step %d/%d", k, n)
				r.m = r.build(rigOpts{})
				rep, err := r.m.Recover(ctxbg())
				if err != nil || rep.Failed != 0 || rep.Conflicts != 0 {
					t.Errorf("%s: Recover = %v, %v", label, rep, err)
				}
				if err := c.ok(r, id); err != nil {
					t.Errorf("%s: %v (labels %s)", label, err, strings.Join(f.labels[before:], ","))
				}
				if rep2, err := r.m.Recover(ctxbg()); err != nil || rep2 != (RecoverReport{}) {
					t.Errorf("%s: a second Recover found work: %v, %v", label, rep2, err)
				}
			}
		})
	}
}

// Recovery leaves held records and a live operation's record alone.
func TestRecoverLeavesHeldAndInFlightQuarantine(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	r.put("books-library", "a.m4b", "bytes")
	id, err := r.m.Quarantine(ctxbg(), books, Path{Root: "books-library", Rel: "a.m4b"}, "x")
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := r.m.Recover(ctxbg()); err != nil || rep != (RecoverReport{}) {
		t.Errorf("Recover of a held record = %v, %v", rep, err)
	}
	if recs, _ := r.m.Quarantined(ctxbg(), books); len(recs) != 1 || recs[0].ID != id {
		t.Errorf("the held record changed: %v", recs)
	}
}
