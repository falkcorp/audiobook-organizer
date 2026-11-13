// file: core/integrations/notify/ulid.go
// version: 1.0.0
// guid: 6a0e3b58-d1f4-4c72-8e95-2c7b4f19a063
// last-edited: 2026-11-13

package notify

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID is a ULID: 48 bits of t's Unix milliseconds, 80 bits read from r,
// as 26 characters of Crockford base32, so IDs sort by creation time to the
// millisecond. It is a fourth copy of audit's, httpmw's and taskstore's
// (R4 keeps sibling packages from sharing one without a new L0 package).
func newULID(t time.Time, r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", fmt.Errorf("notify: notification ID entropy: %w", err)
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
