// file: core/storage/keys/keys.go
// version: 1.0.0
// guid: 4b586f43-bf5a-4ebd-b567-bed548e0c89c
// last-edited: 2026-11-05

// Package keys is the core key encoding (05-storage §5S.3.3): pure functions
// that build order-preserving tuple keys from segments.
//
// Join writes each segment with every 0x00 byte escaped as 0x00 0xFF and ends
// it with the terminator 0x00 0x01 (FoundationDB tuple-layer style). So for
// any two segment lists the byte order of the joined keys equals the
// lexicographic order of the lists: a segment that is a prefix of another
// sorts first (0x01 sorts below 0xFF and below every non-zero content byte),
// and an embedded zero sorts below every other content byte. Every key that
// extends a list lies in [Prefix(list), PrefixEnd(Prefix(list))). Any byte may
// appear in a segment.
//
// Layer L0 (03 R4): standard library only, no package-level state.
package keys

import (
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

const (
	esc  = 0x00 // first byte of both two-byte codes
	term = 0x01 // esc, term ends a segment
	lit0 = 0xFF // esc, lit0 is a literal 0x00 inside a segment
)

// Seg is one key segment.
type Seg []byte

// Str is a segment holding the bytes of s.
func Str(s string) Seg { return Seg(s) }

// Bytes is a segment holding a copy of b.
func Bytes(b []byte) Seg { return append(Seg(nil), b...) }

// U64 is an 8-byte big-endian segment, so it sorts numerically.
func U64(n uint64) Seg {
	return binary.BigEndian.AppendUint64(nil, n)
}

// Time is t's UnixNano as 8 big-endian bytes with the sign bit flipped, so
// times before 1970 sort before times after it.
func Time(t time.Time) Seg {
	return binary.BigEndian.AppendUint64(nil, uint64(t.UnixNano())^(1<<63))
}

// Join encodes segs as one key.
func Join(segs ...Seg) []byte {
	n := 0
	for _, s := range segs {
		n += len(s) + 2
	}
	out := make([]byte, 0, n)
	for _, s := range segs {
		for _, b := range s {
			if b == esc {
				out = append(out, esc, lit0)
				continue
			}
			out = append(out, b)
		}
		out = append(out, esc, term)
	}
	return out
}

// Prefix is Join under a name that says what the key is for: the lower bound
// of every key that extends segs.
func Prefix(segs ...Seg) []byte { return Join(segs...) }

// ErrMalformed is returned by Split for bytes that Join cannot have produced.
var ErrMalformed = errors.New("keys: malformed tuple key")

// Split is the inverse of Join (census and diagnostics rendering).
func Split(key []byte) ([]Seg, error) {
	var segs []Seg
	cur := Seg{}
	for i := 0; i < len(key); i++ {
		b := key[i]
		if b != esc {
			cur = append(cur, b)
			continue
		}
		if i+1 >= len(key) {
			return nil, fmt.Errorf("%w: escape byte at end (offset %d)", ErrMalformed, i)
		}
		i++
		switch key[i] {
		case term:
			segs = append(segs, cur)
			cur = Seg{}
		case lit0:
			cur = append(cur, esc)
		default:
			return nil, fmt.Errorf("%w: escape followed by 0x%02x (offset %d)", ErrMalformed, key[i], i)
		}
	}
	if len(cur) > 0 {
		return nil, fmt.Errorf("%w: last segment has no terminator", ErrMalformed)
	}
	return segs, nil
}

// PrefixEnd returns the smallest key greater than every key that starts with
// prefix, or nil when there is none (an empty prefix, or one made only of
// 0xFF bytes).
func PrefixEnd(prefix []byte) []byte {
	end := append([]byte(nil), prefix...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xFF {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

// maxNameLen is ValidName's length limit.
const maxNameLen = 32

// ValidName checks keyspace and Sub names: [a-z0-9_]{1,32}. '/' (0x2F) sorts
// below every allowed byte, so "m/tv/series/" keys stay contiguous and ordered.
func ValidName(s string) error {
	if len(s) == 0 || len(s) > maxNameLen {
		return fmt.Errorf("keys: name %q must be 1 to %d characters", s, maxNameLen)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
			continue
		}
		return fmt.Errorf("keys: name %q has %q at offset %d; allowed are a-z, 0-9 and _", s, c, i)
	}
	return nil
}
