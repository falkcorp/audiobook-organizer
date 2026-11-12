// file: core/library/recover_test.go
// version: 1.0.1
// guid: d6fba451-bfd4-4e6c-bd41-649146b9f034
// last-edited: 2026-11-11

package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/scheduler"
	"github.com/falkcorp/mediacore/tasks"
)

// scenario is one transfer shape the crash matrix kills at every durable step.
type scenario struct {
	name     string
	setup    func(r *rig)
	src, dst Path
	opts     TransferOptions
	inject   func(string) error
	orig     string // the source's bytes
	oldDst   string // the destination's bytes before a Replace; "" = none
	isMove   bool
	isLink   bool
}

func matrixScenarios() []scenario {
	src := Path{Root: "import-1", Rel: "Arkham/The Killing Joke.m4b"}
	dst := Path{Root: "books-library", Rel: "Alan Moore/The Killing Joke.m4b"}
	replace := NewTransferOptions(Copy, PermLibrary, VerifySHA256)
	replace.Replace = &ReplaceSpec{ExpectSHA256: sum("the old edition of the Joke")}
	replace.MkdirAll = true
	replMove := NewTransferOptions(Move, PermLibrary, VerifySHA256)
	replMove.Replace = &ReplaceSpec{ExpectSHA256: sum("the old edition of the Joke")}
	mk := func(old string) func(r *rig) {
		return func(r *rig) {
			r.put("import-1", src.Rel, "the bytes of the Joke, one laugh at a time")
			if old != "" {
				r.put("books-library", dst.Rel, old)
			}
		}
	}
	const orig = "the bytes of the Joke, one laugh at a time"
	mv := move()
	mv.MkdirAll = true
	link := NewTransferOptions(Hardlink, PermLibrary, VerifySize)
	link.MkdirAll = true
	return []scenario{
		{name: "move across a device boundary", setup: mk(""), src: src, dst: dst, opts: mv, inject: crossRoots, orig: orig, isMove: true},
		{name: "move by rename", setup: mk(""), src: src, dst: dst, opts: mv, orig: orig, isMove: true},
		{name: "copy", setup: mk(""), src: src, dst: dst, opts: copyOpts(), orig: orig},
		{name: "copy replacing", setup: mk("the old edition of the Joke"), src: src, dst: dst, opts: replace, orig: orig, oldDst: "the old edition of the Joke"},
		{name: "cross-device move replacing", setup: mk("the old edition of the Joke"), src: src, dst: dst, opts: withMkdir(replMove), inject: crossRoots, orig: orig, oldDst: "the old edition of the Joke", isMove: true},
		{name: "move by rename replacing", setup: mk("the old edition of the Joke"), src: src, dst: dst, opts: withMkdir(replMove), orig: orig, oldDst: "the old edition of the Joke", isMove: true},
		{name: "hardlink", setup: mk(""), src: src, dst: dst, opts: link, orig: orig, isLink: true},
	}
}

// crossRoots makes only the no-clobber rename out of the import root report a
// device boundary: the rename that sets a replaced file aside stays inside the
// library root and works, as it does on a real second device.
func crossRoots(label string) error {
	if strings.HasPrefix(label, "import-1:rename:") {
		return ErrCrossDevice
	}
	return nil
}

func withMkdir(o TransferOptions) TransferOptions { o.MkdirAll = true; return o }

// tmpFiles lists library's temp and aside files under both roots, with the
// content of each.
func (r *rig) strays() []string {
	return append(r.leftovers("books-library"), r.leftovers("import-1")...)
}

// copies reports every file under the two roots whose content equals want.
func (r *rig) copies(want string) []string {
	var out []string
	for _, id := range []RootID{"books-library", "import-1"} {
		_ = filepath.WalkDir(r.dirs[id], func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && !strings.Contains(p, "/"+DefaultAppDir+"/") {
				if b, rerr := os.ReadFile(p); rerr == nil && string(b) == want {
					out = append(out, p)
				}
			}
			return nil
		})
	}
	return out
}

// checkSafe is what must hold at every instant, mid-crash included: one
// complete copy of the source's bytes exists, and a replaced file's old bytes
// exist until the new bytes are in place.
func (s scenario) checkSafe(t *testing.T, r *rig, label string) {
	t.Helper()
	if len(r.copies(s.orig)) == 0 {
		t.Errorf("%s: no complete copy of the source's bytes exists anywhere", label)
	}
	if s.oldDst != "" {
		got, ok := r.read(s.dst.Root, s.dst.Rel)
		newInPlace := ok && got == s.orig
		if !newInPlace && len(r.copies(s.oldDst)) == 0 {
			t.Errorf("%s: the replaced file's bytes are gone and the new bytes are not in place", label)
		}
	}
	if got, ok := r.read(s.dst.Root, s.dst.Rel); ok && got != s.orig && got != s.oldDst {
		t.Errorf("%s: the destination holds partial or foreign bytes %q", label, got)
	}
}

// checkSettled is the end state after recovery: done or rolled back, never half.
func (s scenario) checkSettled(t *testing.T, r *rig, label string) {
	t.Helper()
	s.checkSafe(t, r, label)
	if l := r.strays(); len(l) != 0 {
		t.Errorf("%s: temp or aside files left: %v", label, l)
	}
	if n := r.journalRows(); n != 0 {
		t.Errorf("%s: %d journal rows left", label, n)
	}
	srcGot, srcOK := r.read(s.src.Root, s.src.Rel)
	dstGot, dstOK := r.read(s.dst.Root, s.dst.Rel)
	if srcOK && srcGot != s.orig {
		t.Errorf("%s: the source changed: %q", label, srcGot)
	}
	done := dstOK && dstGot == s.orig
	switch {
	case s.isMove && done:
		if srcOK {
			t.Errorf("%s: a finished Move left the source", label)
		}
	case s.isMove:
		if !srcOK {
			t.Errorf("%s: a rolled back Move lost the source (the destination is %q, %v)", label, dstGot, dstOK)
		}
	default:
		if !srcOK {
			t.Errorf("%s: a copy lost the source", label)
		}
	}
	if !done {
		// Rolled back: the destination is what it was.
		if s.oldDst == "" && dstOK {
			t.Errorf("%s: a rolled back transfer left a destination: %q", label, dstGot)
		}
		if s.oldDst != "" && (!dstOK || dstGot != s.oldDst) {
			t.Errorf("%s: a rolled back Replace did not restore the old file: %q %v", label, dstGot, dstOK)
		}
	}
	if s.isLink && done {
		si, _ := os.Stat(r.abs(s.src.Root, s.src.Rel))
		di, _ := os.Stat(r.abs(s.dst.Root, s.dst.Rel))
		if !os.SameFile(si, di) {
			t.Errorf("%s: the hardlink is not the source's inode", label)
		}
	}
}

func (s scenario) newRig(t *testing.T, f *faults) *rig {
	t.Helper()
	r, _ := transferRig(t, rigOpts{faults: f})
	f.inject = s.inject
	s.setup(r)
	return r
}

// TestTransferRecoveryFromJournal kills the process at every durable step of
// every transfer shape (each filesystem mutation, each file write and fsync,
// each journal write), then boots a new manager over what is left and runs
// recovery. Every state must recover to done or rolled back, with nothing lost
// at any instant and no temp left. Recovery is then killed at each of its own
// steps and run again.
func TestTransferRecoveryFromJournal(t *testing.T) {
	for _, s := range matrixScenarios() {
		t.Run(s.name, func(t *testing.T) {
			// Dry run: how many durable steps does the transfer have?
			dry := &faults{}
			dr := s.newRig(t, dry)
			if _, err := dr.m.Transfer(ctxbg(), books, s.src, s.dst, s.opts); err != nil {
				t.Fatalf("dry run: %v", err)
			}
			n := dry.steps()
			if n < 4 {
				t.Fatalf("only %d steps in a dry run: %v", n, dry.labels)
			}
			s.checkSettled(t, dr, "dry run")
			for k := 1; k <= n; k++ {
				t.Run(fmt.Sprintf("kill-%02d", k), func(t *testing.T) {
					t.Parallel() // each kill point is its own rig, store and directories
					f := &faults{killAt: k}
					r := s.newRig(t, f)
					_, err := r.m.Transfer(ctxbg(), books, s.src, s.dst, s.opts)
					label := fmt.Sprintf("killed at step %d/%d (%s)", k, n, f.labels[len(f.labels)-1])
					if err == nil && k < n-1 {
						// Only the journal's final delete may fail without failing the call.
						t.Errorf("%s: Transfer reported success", label)
					}
					s.checkSafe(t, r, label+" before recovery")
					r.m = r.build(rigOpts{}) // a new process over what the old one left
					rep, rerr := r.m.Recover(ctxbg())
					if rerr != nil || rep.Failed != 0 || rep.Conflicts != 0 || rep.Blocked != 0 {
						t.Errorf("%s: Recover = %v, %v", label, rep, rerr)
					}
					s.checkSettled(t, r, label+" after recovery")
					if rep2, err := r.m.Recover(ctxbg()); err != nil || rep2 != (RecoverReport{}) {
						t.Errorf("%s: a second Recover found work: %v, %v", label, rep2, err)
					}
				})
			}
		})
	}
}

// Recovery is itself crash-safe: killed at each of its own steps, a later pass
// still settles the transfer.
func TestTransferRecoveryIsCrashSafe(t *testing.T) {
	for _, s := range matrixScenarios() {
		t.Run(s.name, func(t *testing.T) {
			dry := &faults{}
			dr := s.newRig(t, dry)
			if _, err := dr.m.Transfer(ctxbg(), books, s.src, s.dst, s.opts); err != nil {
				t.Fatal(err)
			}
			n := dry.steps()
			for k := 1; k <= n; k++ {
				t.Run(fmt.Sprintf("kill-%02d", k), func(t *testing.T) {
					t.Parallel()
					// Crash the transfer at k, then crash the first recovery at j.
					probe := &faults{killAt: k}
					pr := s.newRig(t, probe)
					_, _ = pr.m.Transfer(ctxbg(), books, s.src, s.dst, s.opts)
					recFaults := &faults{}
					pr.m = pr.build(rigOpts{faults: recFaults})
					if _, err := pr.m.Recover(ctxbg()); err != nil {
						t.Fatalf("k=%d: probe recovery: %v", k, err)
					}
					m := recFaults.steps()
					for j := 1; j <= m; j++ {
						f := &faults{killAt: k}
						r := s.newRig(t, f)
						_, _ = r.m.Transfer(ctxbg(), books, s.src, s.dst, s.opts)
						rf := &faults{killAt: j}
						r.m = r.build(rigOpts{faults: rf})
						_, _ = r.m.Recover(ctxbg())
						label := fmt.Sprintf("transfer killed at %d, recovery killed at %d/%d", k, j, m)
						s.checkSafe(t, r, label+" mid-recovery")
						r.m = r.build(rigOpts{})
						if rep, err := r.m.Recover(ctxbg()); err != nil || rep.Failed != 0 || rep.Conflicts != 0 {
							t.Errorf("%s: final Recover = %v, %v", label, rep, err)
						}
						s.checkSettled(t, r, label)
					}
				})
			}
		})
	}
}

// A cross-device Move whose source changed after the copy keeps the source:
// the destination is verified, and the source is removed only if it still has
// the digest that was copied.
func TestTransferRecoveryKeepsAChangedSource(t *testing.T) {
	r, f := transferRig(t, rigOpts{faults: &faults{}})
	f.inject = crossRoots
	src := r.put("import-1", "Arkham/a.m4b", "the original bytes")
	dst := Path{Root: "books-library", Rel: "a.m4b"}
	// Die right after the publish is journaled: before the source goes.
	pub := 0
	dry, _ := transferRig(t, rigOpts{faults: &faults{}})
	dry.f.inject = crossRoots
	dry.put("import-1", "Arkham/a.m4b", "the original bytes")
	if _, err := dry.m.Transfer(ctxbg(), books, src, dst, move()); err != nil {
		t.Fatal(err)
	}
	for i, l := range dry.f.labels {
		if strings.Contains(l, "remove:Arkham/a.m4b") {
			pub = i + 1
		}
	}
	if pub == 0 {
		t.Fatalf("no source removal in %v", dry.f.labels)
	}
	f.killAt = pub // the removal of the source is the step that does not happen
	if _, err := r.m.Transfer(ctxbg(), books, src, dst, move()); err == nil {
		t.Fatal("the kill did not stop the transfer")
	}
	if !r.exists("import-1", "Arkham/a.m4b") || !r.exists("books-library", "a.m4b") {
		t.Fatalf("setup: both copies should exist (src %v, dst %v)", r.exists("import-1", "Arkham/a.m4b"), r.exists("books-library", "a.m4b"))
	}
	// Someone appends to the source before recovery runs.
	if err := os.WriteFile(r.abs("import-1", "Arkham/a.m4b"), []byte("the original bytes, and a new chapter"), 0o664); err != nil {
		t.Fatal(err)
	}
	r.m = r.build(rigOpts{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Conflicts != 1 {
		t.Fatalf("Recover = %v, %v; want one conflict", rep, err)
	}
	if got, _ := r.read("import-1", "Arkham/a.m4b"); got != "the original bytes, and a new chapter" {
		t.Errorf("the changed source was removed or rewritten: %q", got)
	}
	if got, _ := r.read("books-library", "a.m4b"); got != "the original bytes" {
		t.Errorf("dst = %q", got)
	}
	if r.journalRows() != 0 {
		t.Error("the undecidable row was not dropped")
	}
}

// A root that is unmounted when recovery runs is waited for, not acted on.
func TestTransferRecoveryWaitsForAnUnmountedRoot(t *testing.T) {
	s := matrixScenarios()[0]
	f := &faults{killAt: 5}
	r := s.newRig(t, f)
	_, _ = r.m.Transfer(ctxbg(), books, s.src, s.dst, s.opts)
	r.m = r.build(rigOpts{})
	if err := os.Remove(r.abs("books-library", DefaultAppDir+"/"+sentinelName)); err != nil {
		t.Fatal(err)
	}
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep.Blocked != 1 {
		t.Fatalf("Recover = %v, %v; want one blocked row", rep, err)
	}
	if r.journalRows() != 1 {
		t.Error("the row of a blocked recovery was dropped")
	}
	if err := r.m.WriteSentinel(ctxbg(), "books-library"); err != nil {
		t.Fatal(err)
	}
	rep, err = r.m.Recover(ctxbg())
	if err != nil || rep.Blocked != 0 {
		t.Fatalf("Recover after the mount returned = %v, %v", rep, err)
	}
	s.checkSettled(t, r, "after the root came back")
}

// Recovery leaves a live transfer's row alone.
func TestRecoverSkipsInFlightRows(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	src := r.put("import-1", "a.m4b", "bytes")
	row := transferRow{ID: r.m.seam.newID(), State: stBegun, Mode: Copy, Src: src, Dst: Path{Root: "books-library", Rel: "a.m4b"}}
	if err := r.m.jrn.put(ctxbg(), row.ID, row); err != nil {
		t.Fatal(err)
	}
	r.m.inflight.Store(row.ID, struct{}{})
	rep, err := r.m.Recover(ctxbg())
	if err != nil || rep != (RecoverReport{}) || r.journalRows() != 1 {
		t.Fatalf("Recover touched an in-flight row: %v, %v, rows %d", rep, err, r.journalRows())
	}
	r.m.inflight.Delete(row.ID)
	if rep, err = r.m.Recover(ctxbg()); err != nil || rep.RolledBack != 1 || r.journalRows() != 0 {
		t.Fatalf("Recover of the abandoned row = %v, %v, rows %d", rep, err, r.journalRows())
	}
}

// The recovery task's definition is valid, its schedule key is the one 05-library
// names, and the scheduler's validation accepts a schedule config at that key.
func TestTransferRecoverTaskDefinition(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	def := TransferRecoverTask(r.m)
	if probs := tasks.Problems(def); len(probs) != 0 {
		t.Fatalf("tasks.Problems: %v", probs)
	}
	if def.Kind() != TransferRecoverKind || string(TransferRecoverKind) != "core.library.transfer-recover" {
		t.Errorf("kind = %q", def.Kind())
	}
	if TransferRecoverScheduleKey != "core.library.schedules.transfer_recover" {
		t.Errorf("schedule key = %q", TransferRecoverScheduleKey)
	}
	declared := func(key string) (bool, string, bool) {
		if key == TransferRecoverScheduleKey {
			return true, "core", true
		}
		return false, "", false
	}
	if err := scheduler.ValidateSchedules([]tasks.Definition{def}, declared); err != nil {
		t.Fatalf("ValidateSchedules: %v", err)
	}
	if err := scheduler.ValidateSchedules([]tasks.Definition{def}, func(string) (bool, string, bool) { return false, "", false }); err == nil {
		t.Error("ValidateSchedules accepted a schedule key nothing declares")
	}
	// A manager that cannot recover yields a definition that fails registration.
	if probs := tasks.Problems(TransferRecoverTask(noRecover{r.m})); len(probs) == 0 {
		t.Error("a definition without Run passed tasks.Problems")
	}
	_ = errors.New
	_ = path.Join
}

// noRecover is a Manager that does not implement Recoverer.
type noRecover struct{ Manager }
