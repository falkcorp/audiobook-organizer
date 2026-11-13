// file: core/internal/logjournal/ulid.go
// version: 1.0.1
// guid: 0b8e4f27-d13a-4c95-a6e1-7f5c2d9b3a48
// last-edited: 2026-11-13

package logjournal

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// maxMS is the largest millisecond a ULID's 48-bit time part holds (year 10889).
const maxMS = 1<<48 - 1

// ulidLen is the text length of a ULID: 128 bits as 26 base32 characters.
const ulidLen = 26

// entropyLen is the ULID's random part in bytes (80 bits).
const entropyLen = 10

// ErrInvalidTime is a record time the journal cannot key: before 1970 or past
// year 9999 (inside the 48-bit millisecond range of a ULID), or, for an import, the zero time (an
// import's key is derived from it, so a substitute would change the key on a
// re-import).
var ErrInvalidTime = errors.New("logjournal: record time is outside 1970 to year 9999, or zero in an import")

// msOf is t's Unix milliseconds, or ErrInvalidTime when it does not fit a ULID.
func msOf(t time.Time) (uint64, error) {
	ms := t.UnixMilli()
	// Year 9999 is where a record's time stops encoding as JSON; it is far
	// inside the ULID's own range.
	if ms < 0 || uint64(ms) > maxMS || t.Year() > 9999 {
		return 0, ErrInvalidTime
	}
	return uint64(ms), nil
}

// makeULID is the 26-character ULID of ms and e: the 48 time bits first, so
// the text sorts by time to the millisecond and then by e.
func makeULID(ms uint64, e [entropyLen]byte) string {
	var b [16]byte
	b[0], b[1], b[2] = byte(ms>>40), byte(ms>>32), byte(ms>>24)
	b[3], b[4], b[5] = byte(ms>>16), byte(ms>>8), byte(ms)
	copy(b[6:], e[:])
	var hi, lo uint64
	for i := range 8 {
		hi = hi<<8 | uint64(b[i])
		lo = lo<<8 | uint64(b[8+i])
	}
	var out [ulidLen]byte
	for i := ulidLen - 1; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:])
}

// parseULID is makeULID's inverse; it accepts only what makeULID writes
// (26 characters of the uppercase alphabet, the first at most '7').
func parseULID(s string) (ms uint64, e [entropyLen]byte, err error) {
	if len(s) != ulidLen {
		return 0, e, fmt.Errorf("logjournal: %d-character ULID, want %d", len(s), ulidLen)
	}
	var hi, lo uint64
	for i := range ulidLen {
		v := strings.IndexByte(crockford, s[i])
		if v < 0 || (i == 0 && v > 7) {
			return 0, e, errors.New("logjournal: not a ULID")
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	var b [16]byte
	for i := range 8 {
		b[i] = byte(hi >> (56 - 8*i))
		b[8+i] = byte(lo >> (56 - 8*i))
	}
	ms = uint64(b[0])<<40 | uint64(b[1])<<32 | uint64(b[2])<<24 | uint64(b[3])<<16 | uint64(b[4])<<8 | uint64(b[5])
	copy(e[:], b[6:])
	return ms, e, nil
}
