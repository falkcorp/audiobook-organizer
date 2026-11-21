// file: core/backup/journal/trim_test.go
// version: 0.1.0
// guid: b7d3e9a1-52c4-4f06-8e1b-3a9c6d0f2e75
// last-edited: 2026-11-20

package journal

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// The cast every trim test revokes, in seq order: line n is cast[n-1].
var trimCast = []string{"robin", "batgirl", "nightwing", "alfred", "catwoman", "oracle", "huntress", "azrael"}

// trimmed is a fixture with a journal of n committed revocation lines (seq 1..n,
// store head at n), open and ready.
func trimmed(t *testing.T, n int) (*fixture, *File) {
	t.Helper()
	fx := newFixture(t)
	j := fx.open(t)
	for i := range n {
		fx.commitRevoke(t, j, trimCast[i])
	}
	if got := fx.head(t).Seq; got != uint64(n) {
		t.Fatalf("setup: head seq %d, want %d", got, n)
	}
	return fx, j
}

func (fx *fixture) rawLines(t *testing.T) [][]byte {
	t.Helper()
	b, err := os.ReadFile(fx.path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
}

func (fx *fixture) noTemp(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(fx.path + trimSuffix); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the trim's temporary file is left behind: %v", err)
	}
}

func seqs(c *Chain) []uint64 {
	var out []uint64
	for _, l := range c.Lines {
		out = append(out, l.Seq)
	}
	return out
}

func sameSeqs(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestTrim_ExactSurvivors pins what a trim to seq 3 of a journal of 6 keeps: the
// lines 4, 5 and 6 as the bytes they were written with, a header that names line
// 3 as its base, and nothing else.
func TestTrim_ExactSurvivors(t *testing.T) {
	fx, j := trimmed(t, 6)
	before := fx.rawLines(t)
	baseMAC, ok := fx.chain(t).MACAt(3)
	if !ok {
		t.Fatal("no mac at seq 3")
	}

	res, err := j.Trim(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := (TrimResult{Requested: 3, Base: 3, Dropped: 3, Kept: 3}); res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	after := fx.rawLines(t)
	if len(after) != 4 {
		t.Fatalf("the file has %d lines, want the header and 3", len(after))
	}
	for i, want := range before[4:] { // before[0] is the header, before[k] is seq k
		if !bytes.Equal(after[1+i], want) {
			t.Errorf("kept line %d is not byte-identical to the original seq %d", i, 4+i)
		}
	}
	c := fx.chain(t)
	if c.Break != nil || c.Torn != 0 {
		t.Fatalf("the trimmed journal does not verify: break %v, torn %d", c.Break, c.Torn)
	}
	if !sameSeqs(seqs(c), []uint64{4, 5, 6}) {
		t.Errorf("surviving seqs = %v, want [4 5 6]", seqs(c))
	}
	if c.Header.BaseSeq != 3 || c.Header.BaseMAC != baseMAC || !c.Header.ChainBase {
		t.Errorf("header base = %d %q chain_base=%v, want 3 %q true", c.Header.BaseSeq, c.Header.BaseMAC, c.Header.ChainBase, baseMAC)
	}
	if got, ok := c.MACAt(3); !ok || got != baseMAC {
		t.Errorf("MACAt(3) = %q, %v: the boundary entry's mac must stay answerable", got, ok)
	}
	if _, ok := c.MACAt(2); ok {
		t.Error("MACAt(2) answers: a line below the cut survived")
	}
	for _, l := range c.Lines {
		if l.Subject.ID == trimCast[0] || l.Subject.ID == trimCast[1] || l.Subject.ID == trimCast[2] {
			t.Errorf("seq %d (%s) survived a trim to seq 3", l.Seq, l.Subject.ID)
		}
	}
	fx.noTemp(t)
}

// TestTrim_BoundaryEntry: the entry at the boundary is dropped but remains the
// base, so a restore whose boundary is exactly it is still covered, and one
// whose boundary is below it is not.
func TestTrim_BoundaryEntry(t *testing.T) {
	fx, j := trimmed(t, 6)
	if _, err := j.Trim(context.Background(), 3); err != nil {
		t.Fatal(err)
	}
	c := fx.chain(t)
	head := fx.head(t)
	at := c.Cover(installID, 3, head, false)
	if !at.OK || len(at.Lines) != 3 {
		t.Fatalf("Cover(boundary = the cut) = %+v, want OK with 3 lines", at)
	}
	if got := at.Lines[0].Seq; got != 4 {
		t.Errorf("first covered line = seq %d, want 4", got)
	}
	below := c.Cover(installID, 2, head, false)
	if below.OK || !strings.Contains(below.Why, "starts after") {
		t.Errorf("Cover(boundary below the cut) = %+v, want refused (journal starts after)", below)
	}
	above := c.Cover(installID, 5, head, false)
	if !above.OK || len(above.Lines) != 1 || above.Lines[0].Seq != 6 {
		t.Errorf("Cover(boundary above the cut) = %+v, want OK with seq 6 only", above)
	}
}

// TestTrim_ToTheHead drops every line; the journal is a header that names the last
// line, and the next append chains from that line's mac.
func TestTrim_ToTheHead(t *testing.T) {
	fx, j := trimmed(t, 4)
	lastMAC := fx.head(t).MAC
	res, err := j.Trim(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 4 || res.Kept != 0 || res.Base != 4 || res.Limit != TrimLimitNone {
		t.Fatalf("result = %+v", res)
	}
	c := fx.chain(t)
	if len(c.Lines) != 0 || c.Header.BaseSeq != 4 || c.Header.BaseMAC != lastMAC || c.PrevMAC() != lastMAC {
		t.Fatalf("header-only journal: lines %d base %d %q prev %q", len(c.Lines), c.Header.BaseSeq, c.Header.BaseMAC, c.PrevMAC())
	}
	fx.commitRevoke(t, j, trimCast[4])
	c = fx.chain(t)
	if c.Break != nil || !sameSeqs(seqs(c), []uint64{5}) {
		t.Fatalf("after one append: break %v seqs %v, want [5]", c.Break, seqs(c))
	}
	if got := fx.head(t).Seq; got != 5 {
		t.Errorf("head seq = %d, want 5", got)
	}
}

// TestTrim_AppendsAfterTrimGoToTheNewFileAndReopen: the File appends to the file
// that is at the journal's name now (not the one it unlinked), the chain
// verifies, a reopen finds it, and a second opener is still locked out.
func TestTrim_AppendsAfterTrimGoToTheNewFileAndReopen(t *testing.T) {
	fx, j := trimmed(t, 5)
	if _, err := j.Trim(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	fx.commitRevoke(t, j, trimCast[5])
	fx.commitRevoke(t, j, trimCast[6])
	c := fx.chain(t)
	if c.Break != nil || !sameSeqs(seqs(c), []uint64{3, 4, 5, 6, 7}) {
		t.Fatalf("after two appends: break %v seqs %v", c.Break, seqs(c))
	}
	if j2, err := OpenFile(context.Background(), fx.opts()); !errors.Is(err, ErrLocked) {
		if j2 != nil {
			_ = j2.Close()
		}
		t.Fatalf("a second opener of the trimmed journal: err = %v, want ErrLocked", err)
	}
	must(t, j.Close())
	re, err := OpenFile(context.Background(), fx.opts())
	if err != nil {
		t.Fatalf("reopen of the trimmed journal: %v", err)
	}
	defer func() { must(t, re.Close()) }()
	fx.commitRevoke(t, re, trimCast[7])
	if c := fx.chain(t); c.Break != nil || c.IntactEnd() != 8 {
		t.Errorf("after the reopen's append: break %v end %d, want 8", c.Break, c.IntactEnd())
	}
}

// TestTrim_NoOpWritesNothing: a trim at or below the base, and a repeated trim,
// leave the file byte-identical and never create the temporary file.
func TestTrim_NoOpWritesNothing(t *testing.T) {
	fx, j := trimmed(t, 5)
	ctx := context.Background()
	orig, err := os.ReadFile(fx.path)
	must(t, err)
	res, err := j.Trim(ctx, 0)
	if err != nil || res.Dropped != 0 || res.Base != 0 {
		t.Fatalf("Trim(0) = %+v, %v", res, err)
	}
	if got, _ := os.ReadFile(fx.path); !bytes.Equal(got, orig) {
		t.Fatal("Trim(0) changed the file")
	}
	if _, err := j.Trim(ctx, 3); err != nil {
		t.Fatal(err)
	}
	once, err := os.ReadFile(fx.path)
	must(t, err)
	for _, seq := range []uint64{3, 2, 1} {
		res, err := j.Trim(ctx, seq)
		if err != nil || res.Dropped != 0 || res.Base != 3 || res.Kept != 2 {
			t.Fatalf("repeat Trim(%d) = %+v, %v; want nothing dropped, base 3, 2 kept", seq, res, err)
		}
		if got, _ := os.ReadFile(fx.path); !bytes.Equal(got, once) {
			t.Fatalf("repeat Trim(%d) rewrote the file", seq)
		}
	}
	fx.noTemp(t)
}

// TestTrim_ASecondTrimBuildsOnTheFirst: the base advances, the earlier cut's
// header is replaced, and the lines stay the original bytes.
func TestTrim_ASecondTrimBuildsOnTheFirst(t *testing.T) {
	fx, j := trimmed(t, 7)
	ctx := context.Background()
	orig := fx.rawLines(t)
	if _, err := j.Trim(ctx, 2); err != nil {
		t.Fatal(err)
	}
	res, err := j.Trim(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 3 || res.Kept != 2 || res.Base != 5 {
		t.Fatalf("second trim = %+v, want 3 dropped (3,4,5), 2 kept, base 5", res)
	}
	got := fx.rawLines(t)
	if len(got) != 3 || !bytes.Equal(got[1], orig[6]) || !bytes.Equal(got[2], orig[7]) {
		t.Fatalf("second trim's lines are not the original seq 6 and 7")
	}
	if c := fx.chain(t); c.Break != nil || c.Header.BaseSeq != 5 {
		t.Fatalf("chain: break %v base %d", c.Break, c.Header.BaseSeq)
	}
}

// TestTrim_Clamps: a trim never drops a line something still points at. Each case
// asks for more than is safe and gets the highest safe cut, with the reason.
func TestTrim_Clamps(t *testing.T) {
	ctx := context.Background()

	t.Run("tail", func(t *testing.T) {
		fx, j := trimmed(t, 4)
		res, err := j.Trim(ctx, 99)
		if err != nil {
			t.Fatal(err)
		}
		if res.Limit != TrimLimitTail || res.Base != 4 || res.Requested != 99 {
			t.Fatalf("result = %+v, want cut at the last line (4), limit tail", res)
		}
		if c := fx.chain(t); c.Break != nil || c.Header.BaseSeq != 4 {
			t.Fatalf("chain: %v base %d", c.Break, c.Header.BaseSeq)
		}
	})

	t.Run("head", func(t *testing.T) {
		// Two lines written whose head the store has not committed: a boot may still
		// have to re-apply them.
		fx, j := trimmed(t, 3)
		for _, u := range trimCast[3:5] {
			if _, err := j.WriteLine(ctx, Revocation{At: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC), Kind: UserDisabled, Subject: userRef(u)}); err != nil {
				t.Fatal(err)
			}
		}
		res, err := j.Trim(ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		if res.Limit != TrimLimitHead || res.Base != 3 || res.Kept != 2 {
			t.Fatalf("result = %+v, want cut at the head (3), limit head, 2 kept", res)
		}
		if c := fx.chain(t); !sameSeqs(seqs(c), []uint64{4, 5}) {
			t.Errorf("seqs = %v, want [4 5]", seqs(c))
		}
	})

	t.Run("unapplied", func(t *testing.T) {
		fx, j := trimmed(t, 6)
		c := fx.chain(t)
		l4 := c.Lines[3]
		b := fx.ks.NewBatch()
		must(t, StageUnapplied(ctx, b, fx.ks, UnappliedOf(l4, false)))
		must(t, b.Commit(ctx, storage.Sync))
		res, err := j.Trim(ctx, 6)
		if err != nil {
			t.Fatal(err)
		}
		if res.Limit != TrimLimitUnapplied || res.Base != 3 {
			t.Fatalf("result = %+v, want cut at 3 (the line before the queued 4), limit unapplied", res)
		}
		if got := fx.chain(t); got.Lines[0].Seq != 4 || got.Lines[0].MAC != l4.MAC {
			t.Errorf("the queued line did not survive intact: %+v", got.Lines[0])
		}
		// Once the drain deleted the entry, the same trim goes through.
		del := fx.ks.NewBatch()
		must(t, StageUnappliedDelete(del, fx.ks, 4))
		must(t, del.Commit(ctx, storage.Sync))
		if res, err = j.Trim(ctx, 6); err != nil || res.Base != 6 || res.Limit != TrimLimitNone {
			t.Fatalf("after the drain: %+v, %v", res, err)
		}
	})

	t.Run("gap", func(t *testing.T) {
		fx, j := trimmed(t, 3)
		since := time.Date(2026, 11, 10, 2, 30, 0, 0, time.UTC)
		b := fx.auth.NewBatch()
		must(t, j.Gap(b, since))
		must(t, b.Commit(ctx, storage.Sync))
		// The closing gap line is seq 4 and the revocation seq 5. The head is
		// committed; the delete of the gap record is not.
		p, err := j.WriteLine(ctx, Revocation{At: since, Kind: UserDisabled, Subject: userRef(trimCast[3])})
		if err != nil {
			t.Fatal(err)
		}
		must(t, CommitHead(ctx, fx.ks, p.head))
		res, err := j.Trim(ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		if res.Limit != TrimLimitGap || res.Base != 3 || res.Kept != 2 {
			t.Fatalf("result = %+v, want cut at 3 (before the gap line), limit gap", res)
		}
		must(t, p.Commit(ctx)) // the record's delete lands
		if res, err = j.Trim(ctx, 5); err != nil || res.Base != 5 || res.Limit != TrimLimitNone {
			t.Fatalf("after the gap record was deleted: %+v, %v", res, err)
		}
	})

	t.Run("restore unhooked, hooked and aborted", func(t *testing.T) {
		fx, j := trimmed(t, 2)
		now := time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC)
		if _, err := j.AppendLine(ctx, RestoreLineOf(RestoreInfo{Kind: RestoreExternal, Source: "gotham-archive", Boundary: 1}, now)); err != nil {
			t.Fatal(err)
		}
		fx.commitRevoke(t, j, trimCast[2])
		fx.commitRevoke(t, j, trimCast[3])
		// Lines: 1,2 revocations; 3 restore; 4,5 revocations.
		res, err := j.Trim(ctx, 5)
		if err != nil {
			t.Fatal(err)
		}
		if res.Limit != TrimLimitRestore || res.Base != 2 {
			t.Fatalf("unhooked restore: %+v, want cut at 2, limit restore", res)
		}
		must(t, MarkHooked(ctx, fx.ks, 3, now, nil))
		if res, err = j.Trim(ctx, 5); err != nil || res.Base != 5 || res.Limit != TrimLimitNone {
			t.Fatalf("hooked restore: %+v, %v", res, err)
		}

		// A restore a restore_aborted line closes needs no hooks.
		fx2, j2 := trimmed(t, 1)
		if _, err := j2.AppendLine(ctx, RestoreLineOf(RestoreInfo{Kind: RestoreExternal, Source: "gotham-archive"}, now)); err != nil {
			t.Fatal(err)
		}
		if _, err := j2.AppendLine(ctx, AbortedLine(2, now)); err != nil {
			t.Fatal(err)
		}
		if res, err = j2.Trim(ctx, 3); err != nil || res.Base != 3 || res.Limit != TrimLimitNone {
			t.Fatalf("aborted restore: %+v, %v", res, err)
		}
		_ = fx2
	})

	t.Run("the lowest of several wins", func(t *testing.T) {
		fx, j := trimmed(t, 6)
		c := fx.chain(t)
		b := fx.ks.NewBatch()
		must(t, StageUnapplied(ctx, b, fx.ks, UnappliedOf(c.Lines[4], true))) // seq 5
		must(t, b.Commit(ctx, storage.Sync))
		res, err := j.Trim(ctx, 50)
		if err != nil || res.Base != 4 || res.Limit != TrimLimitUnapplied {
			t.Fatalf("result = %+v, %v; want cut at 4 by the unapplied entry, not 6 by the tail", res, err)
		}
	})
}

// TestTrim_ClosedAndBrokenFilesAreRefused.
func TestTrim_ClosedAndBrokenFilesAreRefused(t *testing.T) {
	ctx := context.Background()
	fx, j := trimmed(t, 3)
	before, err := os.ReadFile(fx.path)
	must(t, err)
	must(t, j.Close())
	if _, err := j.Trim(ctx, 2); !errors.Is(err, ErrClosed) {
		t.Errorf("Trim on a closed File: %v, want ErrClosed", err)
	}
	if got, _ := os.ReadFile(fx.path); !bytes.Equal(got, before) {
		t.Error("a refused trim changed the file")
	}

	fx2 := newFixture(t)
	fx2.write(t, "not a journal header")
	must(t, os.Chmod(fx2.path, 0o600))
	bj, err := OpenFile(ctx, fx2.opts())
	if !errors.Is(err, ErrCorrupt) || bj == nil {
		t.Fatalf("setup: OpenFile = %v, %v", bj, err)
	}
	defer func() { must(t, bj.Close()) }()
	if _, err := bj.Trim(ctx, 1); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Trim on a broken File: %v, want the open's ErrCorrupt", err)
	}
	if got := strings.Join(fx2.lines(t), "\n"); got != "not a journal header" {
		t.Errorf("a refused trim rewrote the broken journal: %q", got)
	}
}

// TestTrim_ARewrittenFileUnderTheLockBreaksTheFile: a file that is not the chain
// this File holds is never rewritten; the File is marked broken.
func TestTrim_ARewrittenFileUnderTheLockBreaksTheFile(t *testing.T) {
	ctx := context.Background()
	fx, j := trimmed(t, 4)
	good, err := os.ReadFile(fx.path)
	must(t, err)

	// A verified chain that is shorter than the File's: lines were dropped.
	short := fx.rawLines(t)
	must(t, os.WriteFile(fx.path, append(bytes.Join(short[:3], []byte("\n")), '\n'), 0o600))
	if _, err := j.Trim(ctx, 1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Trim over a truncated file: %v, want ErrCorrupt", err)
	}
	if _, err := j.AppendLine(ctx, Revocation{Kind: UserDisabled, Subject: userRef("joker")}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Append after the refused trim: %v, want the File broken (ErrCorrupt)", err)
	}
	if got, _ := os.ReadFile(fx.path); bytes.Equal(got, good) {
		t.Error("setup: the file was not rewritten")
	}
	fx.noTemp(t)
}

// TestTrim_RetiredKeyAfterTheCut (DATA3-13): kept lines keep the kid they were
// signed under. After a rotation and a trim that drops every line of the old
// key, the old key can be retired and the journal still verifies; before the trim
// it could not be.
func TestTrim_RetiredKeyAfterTheCut(t *testing.T) {
	ctx := context.Background()
	fx, j := trimmed(t, 3) // seq 1..3 under d1
	fx.keys.rotate("d2")
	fx.commitRevoke(t, j, trimCast[3]) // 4 under d2
	fx.commitRevoke(t, j, trimCast[4]) // 5 under d2
	if _, err := j.Trim(ctx, 3); err != nil {
		t.Fatal(err)
	}
	fx.keys.retire("d1")
	c := fx.chain(t)
	if c.Break != nil || !sameSeqs(seqs(c), []uint64{4, 5}) {
		t.Fatalf("after the trim and retiring d1: break %v seqs %v", c.Break, seqs(c))
	}
	if c.Header.KID != "d2" || c.Lines[0].KID != "d2" {
		t.Errorf("kids: header %q line %q, want d2", c.Header.KID, c.Lines[0].KID)
	}
	must(t, j.Close())
	re, err := OpenFile(ctx, fx.opts())
	if err != nil {
		t.Fatalf("reopen with d1 retired: %v", err)
	}
	must(t, re.Close())

	// A trim that keeps a d1 line still needs d1: the kid is in use.
	fx2, j2 := trimmed(t, 3)
	fx2.keys.rotate("d2")
	fx2.commitRevoke(t, j2, trimCast[3])
	if _, err := j2.Trim(ctx, 1); err != nil {
		t.Fatal(err)
	}
	fx2.keys.retire("d1")
	if c := fx2.chain(t); c.Break == nil {
		t.Error("lines 2 and 3 are signed under d1: retiring d1 must break the chain, so d1 stays in use until they are gone")
	}
}

// TestTrim_HeaderWireFormat: a header that is not a trim's has no chain_base on
// the wire (so every existing journal's bytes and macs are unchanged), a trim's
// has it, and a header claiming chain_base without a base is refused.
func TestTrim_HeaderWireFormat(t *testing.T) {
	fx, j := trimmed(t, 3)
	if bytes.Contains(fx.rawLines(t)[0], []byte("chain_base")) {
		t.Fatalf("an ordinary header carries chain_base: %s", fx.rawLines(t)[0])
	}
	if _, err := j.Trim(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fx.rawLines(t)[0], []byte(`"chain_base":true`)) {
		t.Fatalf("the trimmed header does not carry chain_base: %s", fx.rawLines(t)[0])
	}
	for name, raw := range map[string]string{
		"no base_seq": `{"seq":0,"install_id":"x","kid":"d1","base_seq":0,"base_mac":"aa","chain_base":true}`,
		"no base_mac": `{"seq":0,"install_id":"x","kid":"d1","base_seq":4,"base_mac":"","chain_base":true}`,
	} {
		if _, err := decodeHeader([]byte(raw)); err == nil {
			t.Errorf("%s: decodeHeader accepted a header that chains from a base it does not name", name)
		}
	}
	if h, err := decodeHeader([]byte(`{"seq":0,"install_id":"x","kid":"d1","base_seq":0,"base_mac":""}`)); err != nil || h.ChainBase {
		t.Errorf("an older header: %+v, %v", h, err)
	}
}

// TestTrim_ARewrittenHeaderThatForgesChainBaseDoesNotVerify: chain_base is under
// the header's mac, so flipping it (to splice lines onto another base) breaks the
// chain at the header.
func TestTrim_ARewrittenHeaderThatForgesChainBaseDoesNotVerify(t *testing.T) {
	fx, j := trimmed(t, 4)
	if _, err := j.Trim(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	lines := fx.rawLines(t)
	lines[0] = bytes.Replace(lines[0], []byte(`"chain_base":true`), []byte(`"chain_base":false`), 1)
	must(t, os.WriteFile(fx.path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600))
	if c := fx.chain(t); c.Break == nil {
		t.Error("a header with chain_base flipped verifies")
	}
}

// TestComponent_Trim: the component refuses before Bind and while held or frozen,
// and passes through once bound and free.
func TestComponent_Trim(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t)
	if _, err := fx.component(t).Trim(ctx, 1); !errors.Is(err, ErrNotBound) {
		t.Fatalf("Trim before Bind: %v, want ErrNotBound", err)
	}
	c := fx.bound(t)
	for i := range 3 {
		fx.commitRevoke(t, c, trimCast[i])
	}
	res, err := c.Trim(ctx, 2)
	if err != nil || res.Base != 2 || res.Kept != 1 {
		t.Fatalf("Trim on a bound component = %+v, %v", res, err)
	}
	c.SetFrozen(true)
	if _, err := c.Trim(ctx, 3); !errors.Is(err, ErrTrimRefused) {
		t.Errorf("Trim while frozen: %v, want ErrTrimRefused", err)
	}
	c.SetFrozen(false)
	if res, err = c.Trim(ctx, 3); err != nil || res.Base != 3 {
		t.Errorf("Trim after unfreezing = %+v, %v", res, err)
	}
	c.Hold(errors.New("a restore is running"))
	if _, err := c.Trim(ctx, 3); !errors.Is(err, ErrTrimRefused) {
		t.Errorf("Trim while held: %v, want ErrTrimRefused", err)
	}
	c.Resume()
	if _, err := c.Trim(ctx, 3); err != nil {
		t.Errorf("Trim after Resume: %v", err)
	}
}

// faultFS wraps an FS: it fails or panics at the operation "label:op" named in
// fail or crash (op is open, write, sync, rename, remove, syncdir), counting how
// often each ran. A panic stands for a power cut: the test recovers, closes
// every descriptor the fs opened (the kernel's would be gone) and looks at the
// disk.
type faultFS struct {
	FS
	mu    sync.Mutex
	cur   string
	fail  map[string]error
	crash map[string]bool
	// flip, when set, makes every write inside the named label land with one
	// byte changed and report success: the silent corruption a verified read
	// back exists to catch.
	flip string
	seen []string
	open  []FSFile
}

type crashed struct{ at string }

func newFaultFS() *faultFS {
	return &faultFS{FS: OSFS(), fail: map[string]error{}, crash: map[string]bool{}}
}

func (f *faultFS) hit(op string) error {
	f.mu.Lock()
	key := f.cur + ":" + op
	f.seen = append(f.seen, key)
	err, doCrash := f.fail[key], f.crash[key]
	f.mu.Unlock()
	if doCrash {
		panic(crashed{key})
	}
	return err
}

func (f *faultFS) Region(label string) func() {
	f.mu.Lock()
	prev := f.cur
	if prev == "" {
		f.cur = label
	}
	f.mu.Unlock()
	exit := f.FS.Region(label)
	return func() {
		exit()
		f.mu.Lock()
		f.cur = prev
		f.mu.Unlock()
	}
}

func (f *faultFS) Open(path string, flag int, perm fs.FileMode) (FSFile, error) {
	if err := f.hit("open"); err != nil {
		return nil, err
	}
	file, err := f.FS.Open(path, flag, perm)
	if err != nil {
		return nil, err
	}
	w := &faultFile{FSFile: file, fs: f}
	f.mu.Lock()
	f.open = append(f.open, w)
	f.mu.Unlock()
	return w, nil
}

func (f *faultFS) Rename(a, b string) error {
	if err := f.hit("rename"); err != nil {
		return err
	}
	return f.FS.Rename(a, b)
}

func (f *faultFS) Remove(p string) error {
	if err := f.hit("remove"); err != nil {
		return err
	}
	return f.FS.Remove(p)
}

func (f *faultFS) SyncDir(d string) error {
	if err := f.hit("syncdir"); err != nil {
		return err
	}
	return f.FS.SyncDir(d)
}

// closeAll closes every file the fs opened, as a dead process's descriptors are.
func (f *faultFS) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range f.open {
		_ = file.Close()
	}
	f.open = nil
}

func (f *faultFS) saw(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seen {
		if s == key {
			return true
		}
	}
	return false
}

type faultFile struct {
	FSFile
	fs *faultFS
}

func (w *faultFile) Write(p []byte) (int, error) {
	if err := w.fs.hit("write"); err != nil {
		return 0, err
	}
	w.fs.mu.Lock()
	flip := w.fs.flip != "" && w.fs.flip == w.fs.cur
	w.fs.mu.Unlock()
	if flip && len(p) > 0 {
		q := bytes.Clone(p)
		q[len(q)/2] ^= 0x01
		return w.FSFile.Write(q)
	}
	return w.FSFile.Write(p)
}

func (w *faultFile) Sync() error {
	if err := w.fs.hit("sync"); err != nil {
		return err
	}
	return w.FSFile.Sync()
}

// openFaulty opens fx's journal over ffs, with n committed lines.
func openFaulty(t *testing.T, n int, ffs *faultFS) (*fixture, *File) {
	t.Helper()
	fx := newFixture(t)
	must(t, os.MkdirAll(fx.dir+"/backups", 0o700))
	o := fx.opts()
	o.FS = ffs
	j, err := OpenFile(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	for i := range n {
		fx.commitRevoke(t, j, trimCast[i])
	}
	return fx, j
}

// TestTrim_FailureBeforeTheRenameLeavesTheJournalUntouchedAndUsable: a failed
// create, write, fsync or rename leaves the old file byte-identical, removes the
// temporary file and leaves the File able to append.
func TestTrim_FailureBeforeTheRenameLeavesTheJournalUntouchedAndUsable(t *testing.T) {
	boom := errors.New("the disk is on fire")
	for _, key := range []string{
		LabelTrimCopy + ":open",
		LabelTrimCopy + ":write",
		LabelTrimCopy + ":sync",
		LabelTrimRename + ":rename",
	} {
		t.Run(key, func(t *testing.T) {
			ffs := newFaultFS()
			fx, j := openFaulty(t, 5, ffs)
			orig, err := os.ReadFile(fx.path)
			must(t, err)
			ffs.fail[key] = boom
			res, err := j.Trim(context.Background(), 3)
			if !errors.Is(err, boom) {
				t.Fatalf("Trim = %+v, %v; want the injected failure", res, err)
			}
			if res.Dropped != 0 || res.Base != 0 {
				t.Errorf("a failed trim reports progress: %+v", res)
			}
			if got, _ := os.ReadFile(fx.path); !bytes.Equal(got, orig) {
				t.Error("the journal changed")
			}
			fx.noTemp(t)
			delete(ffs.fail, key)
			fx.commitRevoke(t, j, trimCast[5])
			if c := fx.chain(t); c.Break != nil || c.IntactEnd() != 6 {
				t.Errorf("append after the failed trim: break %v end %d", c.Break, c.IntactEnd())
			}
			// And the trim works once the disk does.
			if res, err := j.Trim(context.Background(), 3); err != nil || res.Base != 3 {
				t.Errorf("retry = %+v, %v", res, err)
			}
		})
	}
}

// TestTrim_ACorruptCopyIsNeverRenamedIntoPlace: a copy that reads back different
// from what was written (a write that reported success and landed wrong) is
// removed, not renamed over the journal, and the journal is untouched and usable.
func TestTrim_ACorruptCopyIsNeverRenamedIntoPlace(t *testing.T) {
	ffs := newFaultFS()
	fx, j := openFaulty(t, 5, ffs)
	orig, err := os.ReadFile(fx.path)
	must(t, err)
	ffs.flip = LabelTrimCopy
	if res, err := j.Trim(context.Background(), 3); err == nil {
		t.Fatalf("Trim = %+v, nil; want the mismatch refused", res)
	}
	if ffs.saw(LabelTrimRename + ":rename") {
		t.Error("the corrupt copy was renamed")
	}
	if got, _ := os.ReadFile(fx.path); !bytes.Equal(got, orig) {
		t.Error("the journal changed")
	}
	fx.noTemp(t)
	ffs.flip = ""
	fx.commitRevoke(t, j, trimCast[5])
	if res, err := j.Trim(context.Background(), 3); err != nil || res.Base != 3 {
		t.Errorf("retry = %+v, %v", res, err)
	}
}

// TestTrim_ADirSyncFailureBreaksTheFile: after the rename the File appends to the
// new file, but a name that is not durable can bring the old file back under lines
// written to this one, so the File is broken until the boot reopens it. The
// trimmed file itself is whole.
func TestTrim_ADirSyncFailureBreaksTheFile(t *testing.T) {
	ffs := newFaultFS()
	fx, j := openFaulty(t, 4, ffs)
	boom := errors.New("the directory cannot be synced")
	ffs.fail[LabelTrimDirSync+":syncdir"] = boom
	res, err := j.Trim(context.Background(), 2)
	if !errors.Is(err, boom) {
		t.Fatalf("Trim = %+v, %v; want the dirsync failure", res, err)
	}
	if res.Base != 2 || res.Dropped != 2 {
		t.Errorf("the rename happened, the result must say so: %+v", res)
	}
	if c := fx.chain(t); c.Break != nil || c.Header.BaseSeq != 2 || !sameSeqs(seqs(c), []uint64{3, 4}) {
		t.Fatalf("the trimmed file: break %v base %d seqs %v", c.Break, c.Header.BaseSeq, seqs(c))
	}
	b := fx.auth.NewBatch()
	if err := j.Append(context.Background(), b, Revocation{Kind: UserDisabled, Subject: userRef("joker")}); err == nil {
		t.Error("Append on the File whose trim could not be made durable succeeded")
	}
	delete(ffs.fail, LabelTrimDirSync+":syncdir")
	must(t, j.Close())
	re, err := OpenFile(context.Background(), fx.opts())
	if err != nil {
		t.Fatalf("reopen after the failed dirsync: %v", err)
	}
	fx.commitRevoke(t, re, trimCast[5])
	must(t, re.Close())
}

// TestTrim_ALeftoverTemporaryFileIsCleared: a crash's leftover is removed, and a
// failure to remove it stops the trim before anything is written.
func TestTrim_ALeftoverTemporaryFileIsCleared(t *testing.T) {
	ctx := context.Background()
	fx, j := trimmed(t, 4)
	must(t, os.WriteFile(fx.path+trimSuffix, []byte("half a header"), 0o600))
	if res, err := j.Trim(ctx, 2); err != nil || res.Base != 2 {
		t.Fatalf("Trim with a leftover = %+v, %v", res, err)
	}
	fx.noTemp(t)
	if c := fx.chain(t); c.Break != nil || c.Header.BaseSeq != 2 {
		t.Fatalf("chain: %v base %d", c.Break, c.Header.BaseSeq)
	}

	ffs := newFaultFS()
	fx2, j2 := openFaulty(t, 4, ffs)
	must(t, os.WriteFile(fx2.path+trimSuffix, []byte("half a header"), 0o600))
	boom := errors.New("cannot remove")
	ffs.fail[LabelTrimClear+":remove"] = boom
	orig, err := os.ReadFile(fx2.path)
	must(t, err)
	if _, err := j2.Trim(ctx, 2); !errors.Is(err, boom) {
		t.Fatalf("Trim = %v, want the clear's failure", err)
	}
	if got, _ := os.ReadFile(fx2.path); !bytes.Equal(got, orig) {
		t.Error("the journal changed although the leftover could not be cleared")
	}
}

// TestTrim_CrashAtEveryStepLeavesAWholeJournal: a power cut at any durable step of
// the trim leaves the old journal or the trimmed one, both verifiable to the same
// last line and mac, openable, and a rerun of the trim finishes it and leaves no
// temporary file.
func TestTrim_CrashAtEveryStepLeavesAWholeJournal(t *testing.T) {
	for _, key := range []string{
		LabelTrimClear + ":remove", // nothing to remove: the call still counts as a step
		LabelTrimCopy + ":open",
		LabelTrimCopy + ":write",
		LabelTrimCopy + ":sync",
		LabelTrimRename + ":rename",
		LabelTrimDirSync + ":syncdir",
	} {
		t.Run(key, func(t *testing.T) {
			ffs := newFaultFS()
			fx, j := openFaulty(t, 5, ffs)
			wantHead := fx.head(t)
			ffs.crash[key] = true
			func() {
				defer func() {
					r := recover()
					c, ok := r.(crashed)
					if !ok || c.at != key {
						t.Fatalf("recovered %v, want the injected crash at %s", r, key)
					}
				}()
				_, _ = j.Trim(context.Background(), 3)
				t.Fatalf("Trim returned although it crashed at %s", key)
			}()
			ffs.closeAll() // the dead process's descriptors and locks are gone

			c := fx.chain(t)
			if c.Break != nil || c.Torn != 0 {
				t.Fatalf("the journal after the crash: break %v torn %d", c.Break, c.Torn)
			}
			if c.IntactEnd() != 5 || c.PrevMAC() != wantHead.MAC {
				t.Fatalf("the journal ends at seq %d mac %q, want 5 %q", c.IntactEnd(), c.PrevMAC(), wantHead.MAC)
			}
			switch c.Header.BaseSeq {
			case 0:
				if len(c.Lines) != 5 {
					t.Errorf("the old journal has %d lines, want 5", len(c.Lines))
				}
			case 3:
				if !sameSeqs(seqs(c), []uint64{4, 5}) {
					t.Errorf("the trimmed journal's seqs = %v, want [4 5]", seqs(c))
				}
			default:
				t.Fatalf("base_seq %d is neither the old journal's 0 nor the cut 3", c.Header.BaseSeq)
			}
			if key == LabelTrimRename+":rename" && c.Header.BaseSeq != 0 {
				t.Error("the crash before the rename must leave the old journal")
			}
			if key == LabelTrimDirSync+":syncdir" && c.Header.BaseSeq != 3 {
				t.Error("the crash after the rename must leave the trimmed journal in this model")
			}

			o := fx.opts()
			re, err := OpenFile(context.Background(), o)
			if err != nil {
				t.Fatalf("OpenFile after the crash: %v", err)
			}
			defer func() { must(t, re.Close()) }()
			res, err := re.Trim(context.Background(), 3)
			if err != nil || res.Base != 3 || res.Kept != 2 {
				t.Fatalf("the rerun = %+v, %v; want it to finish at base 3", res, err)
			}
			fx.noTemp(t)
			if c := fx.chain(t); c.Break != nil || c.Header.BaseSeq != 3 || c.IntactEnd() != 5 {
				t.Errorf("after the rerun: break %v base %d end %d", c.Break, c.Header.BaseSeq, c.IntactEnd())
			}
			fx.commitRevoke(t, re, trimCast[5])
		})
	}
}

// TestTrim_ConcurrentAppendsAreNotLost: appends racing a trim all land, in one
// chain, in either file (the trim holds the mutex across the whole rewrite).
func TestTrim_ConcurrentAppendsAreNotLost(t *testing.T) {
	fx, j := trimmed(t, 3)
	ctx := context.Background()
	const n = 20
	var wg sync.WaitGroup
	var appendMu sync.Mutex // concurrent revocations serialise at the head's commit, as callers' retries do
	errs := make(chan error, n+5)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			appendMu.Lock()
			defer appendMu.Unlock()
			b := fx.auth.NewBatch()
			defer func() { _ = b.Close() }()
			r := Revocation{At: time.Date(2026, 11, 10, 5, 0, 0, 0, time.UTC), Kind: UserDisabled, Subject: userRef(trimCast[i%len(trimCast)])}
			if err := j.Append(ctx, b, r); err != nil {
				errs <- err
				return
			}
			errs <- b.Commit(ctx, storage.Sync)
		}()
	}
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := ReadHead(ctx, fx.ks)
			if err == nil {
				_, err = j.Trim(ctx, h.Seq)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	c := fx.chain(t)
	if c.Break != nil || c.IntactEnd() != 3+n {
		t.Fatalf("chain: break %v end %d, want %d", c.Break, c.IntactEnd(), 3+n)
	}
	if got := fx.head(t).Seq; got != uint64(3+n) {
		t.Errorf("head = %d, want %d", got, 3+n)
	}
	fx.noTemp(t)
}
