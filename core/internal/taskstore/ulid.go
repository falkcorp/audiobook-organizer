// file: core/internal/taskstore/ulid.go
// version: 1.0.0
// guid: d2da7161-0635-4e11-93ac-cd2694856019
// last-edited: 2026-11-10

package taskstore

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewULID is a ULID: 48 bits of t's Unix milliseconds, 80 bits read from r,
// as 26 characters of Crockford base32, so IDs sort by creation time to the
// millisecond. It is a copy of audit's (L4) and httpmw's (L6): R4 keeps the
// three packages from sharing one without a new L0 package.
func NewULID(t time.Time, r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", fmt.Errorf("taskstore: run ID entropy: %w", err)
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(t.UnixMilli()))
	copy(b[:6], ts[2:])
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}
