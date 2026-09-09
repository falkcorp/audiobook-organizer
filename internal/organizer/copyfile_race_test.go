// file: internal/organizer/copyfile_race_test.go
// version: 1.3.0
// guid: 3f9c2a7e-6b41-4d58-9e02-7c1a5d8f4b36
// last-edited: 2026-09-09

package organizer

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Two organize workers targeting the SAME destination (two same-titled books,
// or one book planned twice) must never share a temp file. Before this test
// copyFile wrote every destination through `dst+".tmp"` with O_TRUNC and
// os.Remove'd it first: in 30 of 30 probe iterations one worker returned
// success while dst held the other worker's bytes, or an interleaving of both,
// at full length — so verifyRenamed's size check passed over corrupt audio.
//
// The contract: exactly one writer wins, dst is byte-identical to the WINNER's
// source, the loser gets an error os.IsExist recognises (the callers' race
// recovery branches on it), and no temp file is left behind.
func TestCopyFile_ConcurrentWritersNeverShareATemp(t *testing.T) {
	o := &Organizer{config: &config.Config{}}
	const size = 1 << 20
	a := bytes.Repeat([]byte{'A'}, size)
	b := bytes.Repeat([]byte{'B'}, size)

	for iter := range 40 {
		dir := t.TempDir()
		srcA := filepath.Join(dir, "a.src")
		srcB := filepath.Join(dir, "b.src")
		dst := filepath.Join(dir, "Book.m4b")
		if err := os.WriteFile(srcA, a, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(srcB, b, 0o644); err != nil {
			t.Fatal(err)
		}

		var start sync.WaitGroup
		start.Add(1)
		var done sync.WaitGroup
		errs := make([]error, 2)
		for i, src := range []string{srcA, srcB} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				errs[i] = o.copyFile(src, dst)
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		for i, err := range errs {
			switch {
			case err == nil:
				wins++
			case os.IsExist(err) || errors.Is(err, fs.ErrExist):
			default:
				t.Fatalf("iter %d writer %d: unexpected error %v", iter, i, err)
			}
		}
		if wins != 1 {
			t.Fatalf("iter %d: %d writers reported success, want exactly 1 (errs=%v)", iter, wins, errs)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		var want []byte
		if errs[0] == nil {
			want = a
		} else {
			want = b
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("iter %d: dst is not the winner's bytes (len=%d first=%q last=%q)", iter, len(got), got[0], got[len(got)-1])
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), tempFileSuffix) {
				t.Fatalf("iter %d: temp file left behind: %s", iter, e.Name())
			}
		}
	}
}

// The temp name must still be something cleanupTempFiles recognises, or a
// crash between copy and finalize leaves an unswept multi-GB file forever.
func TestCopyFile_TempNameIsSweptByCleanup(t *testing.T) {
	dir := t.TempDir()
	o := &Organizer{config: &config.Config{RootDir: dir}}
	tmp := o.tempPathFor(filepath.Join(dir, "Book.m4b"))
	if !strings.HasPrefix(tmp, filepath.Join(dir, "Book.m4b")) || !strings.HasSuffix(tmp, tempFileSuffix) {
		t.Fatalf("temp name %q must sit beside dst and end in %q", tmp, tempFileSuffix)
	}
	if o.tempPathFor(filepath.Join(dir, "Book.m4b")) == tmp {
		t.Fatalf("two temp names for the same dst must differ (got %q twice)", tmp)
	}
	if err := os.WriteFile(tmp, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := o.cleanupTempFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("cleanupTempFiles did not sweep %s: %v", tmp, err)
	}
}

// A destination that already exists is refused, never replaced — and the
// refusal is the os.IsExist shape the organize loop's recovery branch expects.
func TestCopyFile_RefusesToReplaceAnExistingDestination(t *testing.T) {
	dir := t.TempDir()
	o := &Organizer{config: &config.Config{}}
	src := filepath.Join(dir, "src.m4b")
	dst := filepath.Join(dir, "Book.m4b")
	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("other book"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := o.copyFile(src, dst)
	if !os.IsExist(err) {
		t.Fatalf("want an os.IsExist error, got %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "other book" {
		t.Fatalf("existing destination was replaced: %q", got)
	}
}

// The second defence, isolated. With the nonce pinned, two writers ARE handed
// the same temp name — the pre-fix situation exactly — and only O_EXCL stands
// between them. The winner's bytes must reach dst intact, and no writer may
// remove a temp it did not create (that would abort the other's in-flight copy).
//
// Renamed 2026-09-09 from TestCopyFile_PinnedNonce_OnlyOneWriterOpensTheTemp,
// which named an invariant that does not hold and made this test flaky in CI.
// Both writers CAN open the temp — just not at the same time. Once the winner
// finalises, its rename frees the temp name, so a slow loser opens it cleanly
// and fails one step later, at finalizeExclusive, with a destination collision.
// That is the interleaving a loaded CI runner hits and a fast laptop does not:
// it failed once on PR #3072's `Go Tests (short, race)` job and passed 30/30
// locally. (Searching for the old name should land here.)
//
// So the loser has TWO legitimate outcomes, and which error is correct depends
// on which one happened:
//
//   - Lost at the O_EXCL temp open — the winner still holds the temp and may be
//     mid-copy, so dst may not exist at all. This must NOT be an fs.ErrExist:
//     copyFile's callers read that as "the DESTINATION is taken" and answer it
//     by adopting whatever is at dst. Reported as a plain "nonce collision".
//   - Lost at finalizeExclusive — reachable only by having PASSED the temp open,
//     which means the winner had already renamed and dst now holds a COMPLETE
//     file. Here fs.ErrExist is exactly right, and adoption is the correct
//     recovery.
//
// The old assertion rejected the second outcome outright, so a real and safe
// interleaving read as a failure. What actually has to hold either way is the
// pairing — a nonce collision must not masquerade as a taken destination — plus
// the invariants below: one winner, intact bytes, no temp left behind.
func TestCopyFile_PinnedNonce_NoTwoWritersHoldTheTempAtOnce(t *testing.T) {
	prev := tempNonce
	tempNonce = func() string { return "pinned" }
	t.Cleanup(func() { tempNonce = prev })

	o := &Organizer{config: &config.Config{}}
	const size = 1 << 20
	a := bytes.Repeat([]byte{'A'}, size)
	b := bytes.Repeat([]byte{'B'}, size)

	for iter := range 20 {
		dir := t.TempDir()
		srcA := filepath.Join(dir, "a.src")
		srcB := filepath.Join(dir, "b.src")
		dst := filepath.Join(dir, "Book.m4b")
		if err := os.WriteFile(srcA, a, 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(srcB, b, 0644); err != nil {
			t.Fatal(err)
		}

		var start, done sync.WaitGroup
		start.Add(1)
		errs := make([]error, 2)
		for i, src := range []string{srcA, srcB} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				errs[i] = o.copyFile(src, dst)
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		var winner []byte
		for i, err := range errs {
			if err == nil {
				wins++
				winner = [][]byte{a, b}[i]
				continue
			}
			// Which of the two legitimate losses happened is read off the
			// error, then that branch's own rule is enforced. The pairing is
			// the point: neither error is acceptable in the other's position.
			switch {
			case strings.Contains(err.Error(), "nonce collision"):
				// Lost at the temp open. dst may not exist yet, so this must
				// not trigger the callers' adopt-the-destination recovery.
				if errors.Is(err, fs.ErrExist) || os.IsExist(err) {
					t.Fatalf("iter %d: a temp-name collision must not look like a taken destination, got %v", iter, err)
				}
			case errors.Is(err, fs.ErrExist) || os.IsExist(err):
				// Lost at finalize, which means the winner had already renamed.
				// dst is complete — asserted for both writers below — so an
				// exists-error is the correct answer here, not a flake.
			default:
				t.Fatalf("iter %d: loser error must be either a nonce collision or a destination collision, got %v", iter, err)
			}
		}
		if wins != 1 {
			t.Fatalf("iter %d: expected exactly one winner, got %d (%v)", iter, wins, errs)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("iter %d: %v", iter, err)
		}
		if !bytes.Equal(got, winner) {
			t.Fatalf("iter %d: destination does not hold the winner's bytes intact (len=%d)", iter, len(got))
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), tempFileSuffix) {
				t.Fatalf("iter %d: temp left behind: %s", iter, e.Name())
			}
		}
	}
}

// The finalize-loss branch above, pinned deterministically.
//
// The concurrent test reaches it only by luck of scheduling — that is precisely
// why the bad assertion survived review and then failed in CI on an unrelated
// PR months later. Serialising the two writers forces the interleaving every
// run: the winner finalises first, freeing the pinned temp name, so the second
// writer opens it cleanly and can only fail at the destination.
//
// This is the assertion that would have caught the mistake locally, and it is
// what keeps the switch above from being a widened assertion that permits
// anything: one branch is now always exercised, on every machine.
func TestCopyFile_PinnedNonce_SecondWriterFailsAtTheDestination(t *testing.T) {
	prev := tempNonce
	tempNonce = func() string { return "pinned" }
	t.Cleanup(func() { tempNonce = prev })

	o := &Organizer{config: &config.Config{}}
	const size = 4096
	a := bytes.Repeat([]byte{'A'}, size)
	b := bytes.Repeat([]byte{'B'}, size)

	dir := t.TempDir()
	srcA := filepath.Join(dir, "a.src")
	srcB := filepath.Join(dir, "b.src")
	dst := filepath.Join(dir, "Book.m4b")
	if err := os.WriteFile(srcA, a, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcB, b, 0644); err != nil {
		t.Fatal(err)
	}

	if err := o.copyFile(srcA, dst); err != nil {
		t.Fatalf("first writer should win outright: %v", err)
	}

	err := o.copyFile(srcB, dst)
	if err == nil {
		t.Fatal("second writer must not silently replace a finished destination")
	}
	// An exists-error, because dst is genuinely taken by a COMPLETE file. The
	// callers' recovery branch keys on this to adopt what is already there.
	if !errors.Is(err, fs.ErrExist) && !os.IsExist(err) {
		t.Fatalf("second writer's error must be recognisable as a taken destination, got %v", err)
	}
	// And NOT reported as a nonce collision: the temp name was free, so the
	// second writer really did open it. Calling this a temp collision would
	// suppress the adoption the caller should be doing.
	if strings.Contains(err.Error(), "nonce collision") {
		t.Fatalf("second writer opened the freed temp; its failure is a destination collision, not a nonce collision: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, a) {
		t.Fatalf("destination no longer holds the first writer's bytes (len=%d)", len(got))
	}
	// The second writer created this temp, so it must clean it up — the mirror
	// of the rule that it must never remove a temp it did not create.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), tempFileSuffix) {
			t.Fatalf("temp left behind: %s", e.Name())
		}
	}
}

// The temp-open-loss branch, pinned deterministically — the other half.
//
// This is the property the old, too-strict assertion was really defending, and
// it must not be lost now that the concurrent test accepts either loss. The
// concurrent test reads which branch happened off the ERROR, so it cannot tell
// a correct finalize-loss ErrExist from a regression that made a temp-open loss
// return a raw ErrExist — it would file the second under the first and pass.
// Both branches therefore get a deterministic test of their own, and the
// concurrent one is left to check the invariants that hold either way.
//
// Standing in for the other writer: the temp already exists and is NOT ours.
// copyFile must fail at the O_EXCL open, must not describe that as a taken
// destination, and must leave the file alone — removing it would truncate a
// live copy out from under the writer that does own it.
func TestCopyFile_PinnedNonce_HeldTempIsNotADestinationCollision(t *testing.T) {
	prev := tempNonce
	tempNonce = func() string { return "pinned" }
	t.Cleanup(func() { tempNonce = prev })

	o := &Organizer{config: &config.Config{}}
	dir := t.TempDir()
	src := filepath.Join(dir, "a.src")
	dst := filepath.Join(dir, "Book.m4b")
	if err := os.WriteFile(src, bytes.Repeat([]byte{'A'}, 4096), 0644); err != nil {
		t.Fatal(err)
	}

	// The other writer's in-flight temp, with bytes we can recognise.
	tempPath := o.tempPathFor(dst)
	otherBytes := []byte("another writer is mid-copy")
	if err := os.WriteFile(tempPath, otherBytes, 0644); err != nil {
		t.Fatal(err)
	}

	err := o.copyFile(src, dst)
	if err == nil {
		t.Fatal("copyFile must not succeed through a temp it does not own")
	}
	if !strings.Contains(err.Error(), "nonce collision") {
		t.Fatalf("a held temp must be reported as a nonce collision, got %v", err)
	}
	// The assertion that matters. An exists-error here sends the caller to its
	// adopt-the-destination branch for a destination that was never written.
	if errors.Is(err, fs.ErrExist) || os.IsExist(err) {
		t.Fatalf("a temp-name collision must not look like a taken destination, got %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("destination must not exist after losing at the temp open (stat err: %v)", statErr)
	}
	// Untouched, byte for byte: not removed, not truncated, not written through.
	held, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatalf("the other writer's temp was removed: %v", err)
	}
	if !bytes.Equal(held, otherBytes) {
		t.Fatalf("the other writer's temp was overwritten (len=%d)", len(held))
	}
}
