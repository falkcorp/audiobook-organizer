// file: core/backup/journal/chain.go
// version: 1.3.0
// guid: 1da2b496-21d3-4218-8a94-2e19379880f4
// last-edited: 2026-11-12

package journal

import (
	"bytes"
	"errors"
	"fmt"
	"iter"
	"os"
	"time"
)

// BreakReason says why a line did not verify.
type BreakReason string

const (
	// BreakMalformed: a complete line that does not decode, has no mac member,
	// or (line 0) is not a header.
	BreakMalformed BreakReason = "malformed"
	// BreakSeq: the line's seq is not the previous line's + 1 (a deleted,
	// reordered or duplicated line).
	BreakSeq BreakReason = "seq"
	// BreakKey: the line names a derive key the keyring cannot give (retired
	// or unknown): it cannot be verified, so it is not trusted.
	BreakKey BreakReason = "key_unavailable"
	// BreakMAC: the mac does not match (an edited, forged or spliced line).
	BreakMAC BreakReason = "mac"
)

// Break is the first line of a journal that does not verify. Seq is the seq
// that line should have carried (the previous line's + 1).
type Break struct {
	Seq    uint64
	Reason BreakReason
	Err    error
}

func (b *Break) Error() string {
	return fmt.Sprintf("journal: the chain breaks at seq %d (%s): %v", b.Seq, b.Reason, b.Err)
}

func (b *Break) Unwrap() error { return b.Err }

// Chain is what ReadChain found: the header, the verified lines up to the
// first break, and what could be told of the rest.
type Chain struct {
	// HasHeader is true when line 0 verified as the header. It is false for an
	// empty file (nothing, or only an incomplete first line: no chain, nothing
	// broken) and for one whose first line did not verify (Break is set).
	HasHeader bool
	Header    Header
	// Lines are the verified lines after the header, in order. They stop at
	// the first break.
	Lines []Line
	// Break is the first line that did not verify, nil when the whole file did.
	Break *Break
	// ClaimedEnd is the highest seq the file claims, trusted or not: the
	// verified end for an intact chain; for a broken one the larger of the
	// last verified seq, the break's and every later decodable line's, and
	// the position of the last complete line. A broken journal's unverified
	// lines are treated as revocations the store may not hold, so this is
	// what "the journal's end" means once the chain is broken.
	ClaimedEnd uint64
	// ValidLen is the byte length of the complete lines; Torn is the length of
	// an incomplete last line after them (cut at the next open: it never
	// returned, so its revocation never committed).
	ValidLen int64
	Torn     int64

	// LastRevocation is the seq of the last verified revocation line (0 when
	// none; gap, restore and restore_aborted lines do not count, SEC4-5).
	LastRevocation uint64
}

// IntactEnd is the seq of the last verified line: the header's base_seq when
// no line follows it.
func (c *Chain) IntactEnd() uint64 {
	if n := len(c.Lines); n > 0 {
		return c.Lines[n-1].Seq
	}
	return c.Header.BaseSeq
}

// PrevMAC is the mac the next line chains from: the last verified line's, or
// the header's own when no line follows it.
func (c *Chain) PrevMAC() string {
	if n := len(c.Lines); n > 0 {
		return c.Lines[n-1].MAC
	}
	return c.Header.MAC
}

// MACAt is the mac of the line at seq: a verified line's, or the header's
// base_mac at base_seq (the last line retention dropped).
func (c *Chain) MACAt(seq uint64) (string, bool) {
	if !c.HasHeader {
		return "", false
	}
	if seq == c.Header.BaseSeq && c.Header.BaseSeq > 0 {
		return c.Header.BaseMAC, true
	}
	if i := seq - c.Header.BaseSeq - 1; seq > c.Header.BaseSeq && i < uint64(len(c.Lines)) {
		return c.Lines[i].MAC, true
	}
	return "", false
}

// LineAt is the verified line at seq. It is false for the header and for a seq
// retention dropped (at or below base_seq), and for one past the verified end:
// the chain says nothing about a line it does not hold, which is what a queued
// entry whose line is gone must be told.
func (c *Chain) LineAt(seq uint64) (Line, bool) {
	if !c.HasHeader || seq <= c.Header.BaseSeq {
		return Line{}, false
	}
	if i := seq - c.Header.BaseSeq - 1; i < uint64(len(c.Lines)) {
		return c.Lines[i], true
	}
	return Line{}, false
}

// Behind reports whether a store whose committed head is head may lack a
// revocation the journal holds, which is §5B.3.6 "Boot re-apply"'s test of
// "head_before is below the journal's last revocation line": an intact chain
// compares with its last revocation line; a broken chain cannot say which of
// its unverified lines are revocations, so it compares with ClaimedEnd; and a
// head below the lines retention dropped (BaseSeq) is behind because those
// lines are gone and cannot be re-applied.
func (c *Chain) Behind(head uint64) bool {
	switch {
	case c.Break != nil:
		// A header that did not verify has no base_seq to trust: ClaimedEnd
		// alone speaks for the file.
		return head < c.ClaimedEnd || (c.HasHeader && head < c.Header.BaseSeq)
	case !c.HasHeader:
		return false // an empty file holds nothing
	}
	return head < c.Header.BaseSeq || head < c.LastRevocation
}

// After yields the verified lines with seq above after, in order.
func (c *Chain) After(after uint64) iter.Seq[Line] {
	return func(yield func(Line) bool) {
		for _, l := range c.Lines {
			if l.Seq > after && !yield(l) {
				return
			}
		}
	}
}

// Has reports whether a verified line with seq above after satisfies f.
func (c *Chain) Has(after uint64, f func(Line) bool) bool {
	for l := range c.After(after) {
		if f(l) {
			return true
		}
	}
	return false
}

// ReadChain walks data, a journal file's bytes, and verifies it with keys: the
// header (chaining from its base_mac), then every line's seq (previous + 1,
// starting after base_seq), its derive key and its mac, chained from the
// previous line's mac. It stops verifying at the first line that fails and
// reports it, so a journal with one bad line still yields the lines before it
// and a Break the caller must treat as untrusted from there on. An
// incomplete last line is not a break (Torn). It never panics on a line
// that names a retired derive key (BreakKey).
func ReadChain(data []byte, keys Keys) *Chain {
	c := &Chain{}
	var lines [][]byte
	off := 0
	for {
		i := bytes.IndexByte(data[off:], '\n')
		if i < 0 {
			break
		}
		lines = append(lines, data[off:off+i])
		off += i + 1
	}
	c.ValidLen, c.Torn = int64(off), int64(len(data)-off)
	if len(lines) == 0 {
		return c
	}
	hdr, err := verifyHeader(lines[0], keys)
	if err != nil {
		c.Break = &Break{Seq: 0, Reason: err.reason, Err: err.err}
		c.ClaimedEnd = claimedEnd(c, lines[1:], 0)
		return c
	}
	c.HasHeader, c.Header = true, hdr
	prevSeq, prevMAC := hdr.BaseSeq, hdr.MAC
	for n, raw := range lines[1:] {
		want := prevSeq + 1
		l, berr := verifyLine(raw, keys, want, prevMAC)
		if berr != nil {
			c.Break = &Break{Seq: want, Reason: berr.reason, Err: berr.err}
			c.ClaimedEnd = claimedEnd(c, lines[1+n:], want)
			return c
		}
		c.Lines = append(c.Lines, l)
		if IsRevocation(l.Kind) {
			c.LastRevocation = l.Seq
		}
		prevSeq, prevMAC = l.Seq, l.MAC
	}
	c.ClaimedEnd = prevSeq
	return c
}

type verifyErr struct {
	reason BreakReason
	err    error
}

func verifyHeader(raw []byte, keys Keys) (Header, *verifyErr) {
	canon, mac, err := unseal(raw)
	if err != nil {
		return Header{}, &verifyErr{BreakMalformed, err}
	}
	h, err := decodeHeader(canon)
	if err != nil {
		return Header{}, &verifyErr{BreakMalformed, err}
	}
	key, err := keyFor(keys, h.KID)
	if err != nil {
		return Header{}, &verifyErr{BreakKey, err}
	}
	if !macEqual(sign(key, h.BaseMAC, canon), mac) {
		return Header{}, &verifyErr{BreakMAC, fmt.Errorf("the header's mac does not verify under derive key %q", h.KID)}
	}
	h.MAC = mac
	return h, nil
}

func verifyLine(raw []byte, keys Keys, want uint64, prevMAC string) (Line, *verifyErr) {
	canon, mac, err := unseal(raw)
	if err != nil {
		return Line{}, &verifyErr{BreakMalformed, err}
	}
	r, kid, err := decodeLine(canon)
	if err != nil {
		return Line{}, &verifyErr{BreakMalformed, err}
	}
	if r.Seq != want {
		return Line{}, &verifyErr{BreakSeq, fmt.Errorf("line has seq %d, want %d", r.Seq, want)}
	}
	key, err := keyFor(keys, kid)
	if err != nil {
		return Line{}, &verifyErr{BreakKey, err}
	}
	if !macEqual(sign(key, prevMAC, canon), mac) {
		return Line{}, &verifyErr{BreakMAC, fmt.Errorf("the mac of seq %d does not verify under derive key %q", want, kid)}
	}
	return Line{Revocation: r, KID: kid, MAC: mac}, nil
}

// claimedEnd is a broken chain's ClaimedEnd: the largest of the last verified
// seq, the seq the break should have carried, every remaining complete line's
// claimed seq (when it decodes) and the position those lines would reach.
func claimedEnd(c *Chain, rest [][]byte, want uint64) uint64 {
	end := max(c.IntactEnd(), want)
	if n := uint64(len(rest)); n > 0 {
		end = max(end, c.IntactEnd()+n)
	}
	for _, raw := range rest {
		if seq, err := decodeSeq(raw); err == nil {
			end = max(end, seq)
		}
	}
	return end
}

// ReadFile reads and verifies the journal at path. A file that does not exist
// is an error satisfying os.ErrNotExist; an empty one yields an empty Chain; a
// path that is not a regular file, or is a symbolic link, is ErrNotRegular (the
// journal is never read through a link planted on its share, for the same
// reason it is never written through one, and a named pipe is refused without
// waiting for a writer); a file over maxJournalBytes is ErrCorrupt.
func ReadFile(path string, keys Keys) (c *Chain, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY|openNoFollow|openNoBlock, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, openError("open", path, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err := regularFile(f); err != nil {
		return nil, err
	}
	data, err := readCapped(f, path)
	if err != nil {
		return nil, err
	}
	return ReadChain(data, keys), nil
}

// Covered is what a restore may re-apply (§5B.3.6 row "Coverage at restore").
type Covered struct {
	// Lines are the verified lines after the boundary; nil when coverage fails.
	Lines []Line
	// OK is true when Lines may be re-applied (they may be empty).
	OK bool
	// Why names the first rule that failed, for the log and the tests.
	Why string
}

// Cover decides a restore's coverage: Revocations is the verified lines after
// boundary only when the journal's install_id equals installID, the chain
// verifies from boundary to the pre-swap store's journal_head (it is intact,
// keeps every line after boundary, is not truncated below that head and holds
// the line the store committed there), no gap lies after boundary (a gap
// line in the journal, or an open journal_gap/ record in the pre-swap store:
// gapOpen), and no line after it is of a kind this build cannot apply. Foreign, host-loss and external restores have no pre-swap store
// and never call it: they get nil without asking.
func (c *Chain) Cover(installID string, boundary uint64, preSwap Head, gapOpen bool) Covered {
	switch {
	case !c.HasHeader:
		return Covered{Why: "the journal is empty or its header does not verify"}
	case c.Header.InstallID != installID:
		return Covered{Why: "the journal belongs to another install"}
	case c.Break != nil:
		return Covered{Why: c.Break.Error()}
	case c.Header.BaseSeq > boundary:
		return Covered{Why: fmt.Sprintf("the journal starts after seq %d (retention dropped the lines after the boundary %d)", c.Header.BaseSeq, boundary)}
	case c.IntactEnd() < preSwap.Seq:
		return Covered{Why: fmt.Sprintf("the journal ends at seq %d, below the pre-swap store's head %d", c.IntactEnd(), preSwap.Seq)}
	case gapOpen:
		return Covered{Why: "the pre-swap store holds an open journal_gap/ record"}
	}
	if preSwap.MAC != "" {
		if got, ok := c.MACAt(preSwap.Seq); !ok || !macEqual(got, preSwap.MAC) {
			return Covered{Why: fmt.Sprintf("the journal's line %d is not the line the pre-swap store committed", preSwap.Seq)}
		}
	}
	if c.Has(boundary, func(l Line) bool { return l.Kind == Gap }) {
		return Covered{Why: "a gap line lies after the boundary"}
	}
	if c.Has(boundary, func(l Line) bool { return IsRevocation(l.Kind) && !KnownKind(l.Kind) }) {
		// A hook applies the lines it is given and skips what it does not know:
		// a line of a newer build's kind would be silently dropped.
		return Covered{Why: "a line after the boundary is of a kind this build cannot apply"}
	}
	var out []Line
	for l := range c.After(boundary) {
		out = append(out, l)
	}
	return Covered{Lines: out, OK: true}
}

// gapSince reads a gap line's Detail[0].
func gapSince(r Revocation) (time.Time, bool) {
	if r.Kind != Gap || len(r.Detail) == 0 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, r.Detail[0])
	return t, err == nil
}
