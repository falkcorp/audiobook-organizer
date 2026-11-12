// file: core/backup/journal/linecap_test.go
// version: 1.0.0
// guid: 43cd1eb5-e9cf-4c46-9967-89f92317829a
// last-edited: 2026-11-11

package journal

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
)

// object is n bytes that look enough like a JSON object for seal and unseal,
// which only split and join: braces around padding.
func object(n int) []byte {
	b := bytes.Repeat([]byte{'x'}, n)
	b[0], b[n-1] = '{', '}'
	return b
}

// TestSeal_RefusesALineOverTheCap: the stored line, newline included, is at
// most maxLineBytes, and seal says so before it sizes a buffer from the
// object's length (the allocation CodeQL flagged as go/allocation-size-overflow).
// The largest object that fits seals and unseals back to itself; one byte more
// is ErrLineTooLarge and yields no line.
func TestSeal_RefusesALineOverTheCap(t *testing.T) {
	mac := strings.Repeat("a", macHexLen)
	biggest := maxLineBytes - sealOverhead

	canon := object(biggest)
	line, err := seal(canon, mac)
	if err != nil {
		t.Fatalf("seal of the largest object that fits: %v", err)
	}
	if len(line) != maxLineBytes || line[len(line)-1] != '\n' {
		t.Fatalf("the sealed line is %d bytes, want exactly %d, newline last", len(line), maxLineBytes)
	}
	back, gotMAC, err := unseal(line[:len(line)-1])
	if err != nil || gotMAC != mac || !bytes.Equal(back, canon) {
		t.Errorf("unseal of the largest line: err %v, mac ok %v, canonical equal %v", err, gotMAC == mac, bytes.Equal(back, canon))
	}

	line, err = seal(object(biggest+1), mac)
	if !errors.Is(err, ErrLineTooLarge) || line != nil {
		t.Errorf("seal of one byte too many = %d bytes, %v, want no line and ErrLineTooLarge", len(line), err)
	}
	if line, err = seal(object(64<<20), mac); !errors.Is(err, ErrLineTooLarge) || line != nil {
		t.Errorf("seal of a 64 MiB object = %d bytes, %v, want no line and ErrLineTooLarge", len(line), err)
	}
}

// TestUnseal_RefusesALineOverTheCap: the same bound on the read side, before the
// buffer for the canonical bytes is sized. The line is given without its
// newline, so the longest one that reads is maxLineBytes-1 long.
func TestUnseal_RefusesALineOverTheCap(t *testing.T) {
	if _, _, err := unseal(object(maxLineBytes)); !errors.Is(err, ErrLineTooLarge) {
		t.Errorf("unseal of a line one byte over the cap = %v, want ErrLineTooLarge", err)
	}
	if _, _, err := unseal(object(maxLineBytes - 1)); errors.Is(err, ErrLineTooLarge) {
		t.Errorf("unseal of the longest line that fits = %v: refused for its size", err)
	}
}

// TestAppend_RefusesAnOversizedRevocation: a revocation whose line would be over
// the cap is not written. Append fails with ErrLineTooLarge and stages nothing,
// as for any append failure (the revoking caller records a gap), and the file
// is not broken: the next ordinary revocation takes the seq the refused one
// would have had.
func TestAppend_RefusesAnOversizedRevocation(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	before := len(fx.lines(t))

	b := fx.auth.NewBatch()
	defer func() { _ = b.Close() }()
	r := Revocation{At: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC), Kind: UserDisabled,
		Subject: entity.Ref{Module: "core", Kind: "user", ID: "dick-grayson"},
		Detail:  []string{strings.Repeat("a", 2*maxLineBytes)}}
	if err := j.Append(t.Context(), b, r); !errors.Is(err, ErrLineTooLarge) {
		t.Fatalf("Append of an oversized revocation = %v, want ErrLineTooLarge", err)
	}
	if got := len(fx.lines(t)); got != before {
		t.Errorf("the journal has %d lines, want %d: nothing is written for a refused line", got, before)
	}
	if st := j.Status(); st.Broken != nil {
		t.Errorf("the file reports broken after a refused line: %v", st.Broken)
	}
	if h := fx.head(t); h.Seq != 0 {
		t.Errorf("head %d after a refused line, want 0", h.Seq)
	}

	fx.commitRevoke(t, j, "barbara-gordon")
	if h := fx.head(t); h.Seq != 1 {
		t.Errorf("head %d after the next revocation, want 1: the refused line took no seq", h.Seq)
	}
	if c := fx.chain(t); c.Break != nil || len(c.Lines) != 1 {
		t.Errorf("chain: break %v, %d lines, want none and 1", c.Break, len(c.Lines))
	}
}

// TestReadChain_AnOversizedLineIsAMalformedBreak: a stored line over the cap
// cannot have been written by this build, so the chain stops verifying there:
// a break of reason malformed that names ErrLineTooLarge, the lines before it
// still verified.
func TestReadChain_AnOversizedLineIsAMalformedBreak(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "barbara-gordon")
	good := fx.lines(t)
	must(t, j.Close())

	fx.write(t, good[0], good[1], strings.Repeat("x", maxLineBytes))
	c := fx.chain(t)
	if c.Break == nil || c.Break.Reason != BreakMalformed || !errors.Is(c.Break.Err, ErrLineTooLarge) {
		t.Fatalf("break = %+v, want malformed naming ErrLineTooLarge", c.Break)
	}
	if len(c.Lines) != 1 || c.Break.Seq != 2 {
		t.Errorf("%d verified line(s), break at seq %d, want 1 and 2", len(c.Lines), c.Break.Seq)
	}
}
