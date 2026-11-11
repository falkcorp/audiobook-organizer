// file: core/backup/journal/chain_test.go
// version: 1.0.0
// guid: 0e0d7a52-76a0-4b2e-9d1b-5b6e3f6a8c14
// last-edited: 2026-11-11

package journal

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// built writes a journal of the header and three revocations (joker, penguin,
// riddler: seqs 1-3) through a real File, and returns its file lines, its
// chain and the store's committed head.
func (fx *fixture) built(t *testing.T) (lines []string, c *Chain, head Head) {
	t.Helper()
	j := fx.open(t)
	for _, u := range []string{"joker", "penguin", "riddler"} {
		fx.commitRevoke(t, j, u)
	}
	must(t, j.Close())
	return fx.lines(t), fx.chain(t), fx.head(t)
}

// TestRevocationJournal_TruncationDetected: a deleted, edited, reordered,
// duplicated, spliced or truncated line, a forged tail and a replaced file
// each break coverage, so a restore treats every key as needing confirmation
// (the boot's reaction is the kernel's; this is the verifier's half). The
// intact journal is the control: it covers the lines after the boundary.
func TestRevocationJournal_TruncationDetected(t *testing.T) {
	fx := newFixture(t)
	lines, intact, head := fx.built(t)
	if head.Seq != 3 || intact.Break != nil || len(intact.Lines) != 3 || head.MAC != intact.Lines[2].MAC {
		t.Fatalf("setup: head %+v, break %v, %d lines", head, intact.Break, len(intact.Lines))
	}
	if cov := intact.Cover(installID, 1, head, false); !cov.OK || len(cov.Lines) != 2 || cov.Lines[0].Seq != 2 || cov.Lines[1].Seq != 3 {
		t.Fatalf("control: Cover = %+v, want seqs 2 and 3", cov)
	}

	// A second journal over the same keys that the third line is spliced from:
	// valid under the key, but chained from other lines.
	other := newFixture(t)
	other.keys = fx.keys
	oj := other.open(t)
	for _, u := range []string{"bane", "scarecrow", "riddler"} {
		other.commitRevoke(t, oj, u)
	}
	must(t, oj.Close())
	foreign := other.lines(t)

	forgedKeys := newTestKeys()
	forgedKeys.secret["d1"] = []byte("not-the-batcave-key")
	forged, _, err := sealLine(forgedKeys, intact.Lines[2].MAC, Revocation{Seq: 4, Kind: UserDisabled, Subject: entity.Ref{Module: "core", Kind: "user", ID: "bane"}})
	must(t, err)

	edited := slices.Clone(lines)
	edited[2] = strings.Replace(edited[2], `"id":"penguin"`, `"id":"alfred"`, 1)
	whitespace := slices.Clone(lines)
	whitespace[2] = strings.Replace(whitespace[2], `"seq":2,`, `"seq":2, `, 1)
	header := slices.Clone(lines)
	header[0] = strings.Replace(header[0], installID, "arkham-install", 1)
	memberAdded := slices.Clone(lines)
	memberAdded[3] = strings.Replace(memberAdded[3], `,"mac":"`, `,"extra":1,"mac":"`, 1)

	cases := []struct {
		name   string
		data   []string
		reason BreakReason
		hasHdr bool
	}{
		{"deleted line", slices.Concat(lines[:2], lines[3:]), BreakSeq, true},
		{"edited line", edited, BreakMAC, true},
		{"edited whitespace", whitespace, BreakMAC, true},
		{"member added", memberAdded, BreakMAC, true},
		{"reordered lines", slices.Concat(lines[:2], []string{lines[3], lines[2]}), BreakSeq, true},
		{"duplicated line", slices.Concat(lines[:3], lines[2:3], lines[3:]), BreakSeq, true},
		{"spliced from another chain", slices.Concat(lines[:3], foreign[3:]), BreakMAC, true},
		{"forged tail line", append(slices.Clone(lines), strings.TrimSuffix(string(forged), "\n")), BreakMAC, true},
		{"edited header", header, BreakMAC, false},
		{"header deleted", lines[1:], BreakMalformed, false},
		{"garbage line", slices.Concat(lines[:3], []string{"not json at all"}), BreakMalformed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx.write(t, tc.data...)
			c := fx.chain(t)
			if c.Break == nil || c.Break.Reason != tc.reason || c.HasHeader != tc.hasHdr {
				t.Fatalf("chain break = %v, header %v; want reason %s, header %v", c.Break, c.HasHeader, tc.reason, tc.hasHdr)
			}
			if cov := c.Cover(installID, 1, head, false); cov.OK || cov.Lines != nil || cov.Why == "" {
				t.Errorf("Cover = %+v, want no coverage with a reason", cov)
			}
			// A store below the journal's claimed end may lack a revocation.
			if !c.Behind(0) || !c.Behind(2) {
				t.Errorf("Behind(0), Behind(2) = %v, %v; want true for a broken chain", c.Behind(0), c.Behind(2))
			}
			// A broken file refuses to be opened and extended.
			fx.openBroken(t, tc.name, ErrCorrupt)
		})
	}

	t.Run("truncated below the store's head", func(t *testing.T) {
		fx.write(t, lines[:3]...) // seqs 0-2; the store committed 3
		c := fx.chain(t)
		if c.Break != nil || c.IntactEnd() != 2 {
			t.Fatalf("a truncated journal is intact up to its end: break %v, end %d", c.Break, c.IntactEnd())
		}
		if cov := c.Cover(installID, 1, head, false); cov.OK {
			t.Errorf("Cover = %+v, want no coverage: the journal ends below the pre-swap store's head", cov)
		}
		fx.openBroken(t, "truncated", ErrCorrupt)
	})

	t.Run("replaced by another valid chain", func(t *testing.T) {
		// foreign is a verified chain of the same keys and install whose seq 3
		// is not the line the store committed.
		fx.write(t, foreign...)
		c := fx.chain(t)
		if c.Break != nil || c.IntactEnd() != 3 {
			t.Fatalf("setup: break %v, end %d", c.Break, c.IntactEnd())
		}
		if cov := c.Cover(installID, 1, head, false); cov.OK || !strings.Contains(cov.Why, "not the line") {
			t.Errorf("Cover = %+v, want refused: the line at the head is not the committed one", cov)
		}
		fx.openBroken(t, "replaced", ErrCorrupt)
	})

	t.Run("a retired key", func(t *testing.T) {
		fx.write(t, lines...)
		retired := newTestKeys()
		retired.retire("d1")
		retired.rotate("d2")
		retired.retire("d1")
		c := ReadChain([]byte(strings.Join(lines, "\n")+"\n"), retired)
		if c.Break == nil || c.Break.Reason != BreakKey || c.HasHeader {
			t.Fatalf("break = %v, want key_unavailable at the header", c.Break)
		}
	})

	t.Run("an open gap or a gap line, another install, an early start", func(t *testing.T) {
		fx.write(t, lines...)
		c := fx.chain(t)
		if cov := c.Cover(installID, 1, head, true); cov.OK {
			t.Errorf("an open journal_gap/ record: Cover = %+v, want refused", cov)
		}
		if cov := c.Cover("arkham-install", 1, head, false); cov.OK {
			t.Errorf("another install: Cover = %+v, want refused", cov)
		}
		j := fx.open(t)
		b := fx.auth.NewBatch()
		must(t, j.Gap(b, time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)))
		must(t, b.Commit(context.Background(), storage.Sync))
		fx.commitRevoke(t, j, "scarecrow") // closes the gap with a gap line at seq 4, the revocation at 5
		must(t, j.Close())
		c = fx.chain(t)
		h := fx.head(t)
		if cov := c.Cover(installID, 3, h, false); cov.OK || !strings.Contains(cov.Why, "gap") {
			t.Errorf("a gap line after the boundary: Cover = %+v, want refused", cov)
		}
		if cov := c.Cover(installID, 4, h, false); !cov.OK || len(cov.Lines) != 1 || cov.Lines[0].Seq != 5 {
			t.Errorf("a boundary past the gap line: Cover = %+v, want seq 5 only", cov)
		}
	})
}

// TestJournalChainSurvivesKidRotation: lines written under d1 then d2 verify
// as one chain (the chain links macs, not keys); a retired kid named by a
// retained line makes verification report the line, never panic.
func TestJournalChainSurvivesKidRotation(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "joker")
	fx.keys.rotate("d2") // rotation while the journal is open
	fx.commitRevoke(t, j, "penguin")
	must(t, j.Close())
	j = fx.open(t) // and across a reopen
	fx.commitRevoke(t, j, "riddler")
	must(t, j.Close())

	c := fx.chain(t)
	if c.Break != nil || len(c.Lines) != 3 {
		t.Fatalf("chain break = %v, %d lines; want one chain of 3 across the rotation", c.Break, len(c.Lines))
	}
	var kids []string
	for _, l := range c.Lines {
		kids = append(kids, l.KID)
	}
	if !slices.Equal(kids, []string{"d1", "d2", "d2"}) || c.Header.KID != "d1" {
		t.Errorf("kids = %v, header kid %q; want d1, d2, d2 under a d1 header", kids, c.Header.KID)
	}
	if h := fx.head(t); h.Seq != 3 || h.MAC != c.Lines[2].MAC {
		t.Errorf("head = %+v", h)
	}

	// d1 is retired while the journal still holds lines signed under it.
	data, err := os.ReadFile(fx.path)
	must(t, err)
	fx.keys.retire("d1")
	var broken *Chain
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ReadChain panicked on a retired kid: %v", r)
			}
		}()
		broken = ReadChain(data, fx.keys)
	}()
	if broken.Break == nil || broken.Break.Reason != BreakKey || broken.Break.Seq != 0 || !strings.Contains(broken.Break.Error(), `"d1"`) {
		t.Fatalf("break = %v, want key_unavailable naming d1", broken.Break)
	}

	// A journal whose header is under d2 but which retains a line under the
	// retired d1: the verified prefix is reported, the d1 line is the break.
	keys := newTestKeys()
	keys.rotate("d2")
	hdrLine, hdr, err := sealHeader(keys, Header{InstallID: installID, KID: "d2", StartedAt: time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)})
	must(t, err)
	l1, mac1, err := sealLine(keys, hdr.MAC, Revocation{Seq: 1, Kind: UserDisabled, Subject: entity.Ref{Module: "core", Kind: "user", ID: "joker"}})
	must(t, err)
	keys.active = "d1"
	keys.secret["d1"] = []byte("alfred-pennyworth-d1")
	l2, _, err := sealLine(keys, mac1, Revocation{Seq: 2, Kind: UserDisabled, Subject: entity.Ref{Module: "core", Kind: "user", ID: "penguin"}})
	must(t, err)
	mixed := bytes.Join([][]byte{hdrLine, l1, l2}, nil)
	keys.retire("d1")
	got := ReadChain(mixed, keys)
	if got.Break == nil || got.Break.Reason != BreakKey || got.Break.Seq != 2 || len(got.Lines) != 1 || got.Lines[0].Seq != 1 || !got.HasHeader {
		t.Fatalf("break = %v, %d verified lines; want line 1 verified and line 2 reported key_unavailable", got.Break, len(got.Lines))
	}
	if cov := got.Cover(installID, 0, Head{Seq: 1, MAC: mac1}, false); cov.OK {
		t.Errorf("a chain that cannot verify its tail covers nothing: %+v", cov)
	}
}

// TestChain_AppendedGapAndBookkeepingLinesAreNotRevocations: gap, restore and
// restore_aborted lines never count as the journal's last revocation (§5B.3.6
// (1)), and a kind this build does not know does.
func TestChain_AppendedGapAndBookkeepingLinesAreNotRevocations(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	ctx := context.Background()
	fx.commitRevoke(t, j, "joker") // seq 1
	for _, k := range []Kind{Gap, Restore, RestoreAborted} {
		if _, err := j.AppendLine(ctx, Revocation{Kind: k, At: time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)}); err != nil {
			t.Fatal(err)
		}
	}
	if h := fx.head(t); h.Seq != 4 {
		t.Fatalf("head = %+v, want seq 4: the bookkeeping lines advance the head", h)
	}
	c := fx.chain(t)
	if c.Break != nil || c.LastRevocation != 1 || c.IntactEnd() != 4 {
		t.Fatalf("LastRevocation = %d, end %d, break %v; want 1, 4", c.LastRevocation, c.IntactEnd(), c.Break)
	}
	if c.Behind(1) || !c.Behind(0) {
		t.Errorf("Behind(1), Behind(0) = %v, %v; want false, true (bookkeeping lines do not count)", c.Behind(1), c.Behind(0))
	}
	if IsRevocation(Gap) || IsRevocation(Restore) || IsRevocation(RestoreAborted) || !IsRevocation(Kind("pokemon.revoked")) || !IsRevocation(UserDeleted) {
		t.Error("IsRevocation: gap, restore and restore_aborted are bookkeeping; every other kind counts")
	}
	if _, ok := c.MACAt(4); !ok {
		t.Error("MACAt(4) not found")
	}
	if _, ok := c.MACAt(5); ok {
		t.Error("MACAt(5) found beyond the end")
	}
}

// TestOpenFile_RefusesOtherInstall: a journal whose header names another
// install is refused (ErrOtherInstall) and never extended or truncated: the
// boot sets such a file aside (the kernel's step 0) instead.
func TestOpenFile_RefusesOtherInstall(t *testing.T) {
	fx := newFixture(t)
	must(t, os.MkdirAll(filepath.Dir(fx.path), 0o700))
	other := fx.opts()
	other.InstallID = "arkham-install"
	oj, err := OpenFile(context.Background(), other)
	must(t, err)
	must(t, oj.Close())
	before, err := os.ReadFile(fx.path)
	must(t, err)
	fx.openBroken(t, "another install", ErrOtherInstall)
	after, err := os.ReadFile(fx.path)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Errorf("the refused open of another install's journal changed the file: %q", after)
	}
	if h := fx.head(t); h.Seq != 0 {
		t.Errorf("head = %+v, want none", h)
	}
}
